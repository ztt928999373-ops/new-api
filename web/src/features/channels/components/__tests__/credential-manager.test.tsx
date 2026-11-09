/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  render,
  screen,
  fireEvent,
  waitFor,
  cleanup,
  within,
} from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { CredentialManager } from '../credential-manager'

vi.mock('@/lib/api', () => ({ api: { request: vi.fn() } }))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (value: string) => value }),
}))
const metadata = {
  id: 'synthetic-reference',
  provider: 'openai',
  status: 'active',
  version: 1,
  revision: 1,
}
beforeEach(() => {
  vi.mocked(api.request).mockReset()
  useAuthStore
    .getState()
    .auth.setUser({ id: 1, username: 'synthetic-admin', role: 100 })
})
afterEach(() => {
  cleanup()
  useAuthStore.getState().auth.setUser(null)
})
function mount(channelId?: number) {
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <CredentialManager channelId={channelId} />
    </QueryClientProvider>
  )
}
test('creation clears the secret and loads safe metadata for a disabled channel', async () => {
  vi.mocked(api.request)
    .mockResolvedValueOnce({ data: { success: true, data: { id: 42 } } })
    .mockResolvedValueOnce({
      data: { success: true, managed: true, data: metadata },
    })
  mount()
  fireEvent.click(screen.getByText('Prepare secure OpenAI channel'))
  fireEvent.change(screen.getByLabelText('Name'), {
    target: { value: 'pilot' },
  })
  fireEvent.change(screen.getByLabelText('Model'), {
    target: { value: 'synthetic-model' },
  })
  fireEvent.change(screen.getByLabelText('New API Key'), {
    target: { value: 'synthetic-secret' },
  })
  fireEvent.click(
    screen.getByRole('button', { name: 'Create disabled channel' })
  )
  await screen.findByText('synthetic-reference')
  expect(screen.getByLabelText('New API Key')).toHaveValue('')
  expect(document.body.textContent).not.toContain('synthetic-secret')
  expect(api.request).toHaveBeenCalledWith(
    expect.objectContaining({
      url: '/api/channel/openai-integration',
      data: expect.objectContaining({ key: 'synthetic-secret' }),
    })
  )
  expect(localStorage.getItem('synthetic-secret')).toBeNull()
})
test('refresh and revocation use current revision and permanently hide replacement', async () => {
  vi.mocked(api.request)
    .mockResolvedValueOnce({
      data: { success: true, managed: true, data: metadata },
    })
    .mockResolvedValueOnce({ data: { success: true } })
    .mockResolvedValueOnce({
      data: {
        success: true,
        managed: true,
        data: { ...metadata, status: 'revoked', revision: 2 },
      },
    })
  mount(42)
  fireEvent.click(screen.getByText('Secure credential'))
  await screen.findByText('synthetic-reference')
  fireEvent.click(screen.getByRole('button', { name: 'Revoke credential' }))
  const confirm = screen.getByRole('alertdialog')
  fireEvent.click(within(confirm).getByRole('button', { name: 'Continue' }))
  await screen.findByText('revoked')
  expect(screen.queryByLabelText('New API Key')).not.toBeInTheDocument()
  expect(api.request).toHaveBeenCalledWith(
    expect.objectContaining({
      url: '/api/channel/credentials/synthetic-reference/status',
      data: { status: 'revoked', revision: 1 },
    })
  )
})
test('legacy channels and insufficient permissions cannot create secure credentials', async () => {
  vi.mocked(api.request).mockResolvedValue({
    data: { success: true, managed: false },
  })
  mount(42)
  fireEvent.click(screen.getByText('Secure credential'))
  await screen.findByRole('alert')
  expect(screen.queryByLabelText('New API Key')).not.toBeInTheDocument()
  cleanup()
  useAuthStore.getState().auth.setUser({ id: 2, username: 'reader', role: 1 })
  mount()
  expect(
    screen.queryByText('Prepare secure OpenAI channel')
  ).not.toBeInTheDocument()
})
test('stale metadata failure clears the replacement key and asks for refresh', async () => {
  vi.mocked(api.request)
    .mockResolvedValueOnce({
      data: { success: true, managed: true, data: metadata },
    })
    .mockRejectedValueOnce({ response: { status: 409 } })
  mount(42)
  fireEvent.click(screen.getByText('Secure credential'))
  await screen.findByText('synthetic-reference')
  fireEvent.change(screen.getByLabelText('New API Key'), {
    target: { value: 'synthetic-replacement' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Replace credential' }))
  await waitFor(() =>
    expect(screen.getByRole('status')).toHaveTextContent(
      'Credential changed. Refresh metadata before retrying.'
    )
  )
  expect(screen.getByLabelText('New API Key')).toHaveValue('')
})

test.each([
  [403, undefined, 'Credential permission denied'],
  [
    503,
    'CHANNEL_CACHE_SYNC_FAILED',
    'Database committed; cache unavailable. Refresh before retrying.',
  ],
  [
    503,
    undefined,
    'Credential operation unavailable. Refresh metadata or contact the operator.',
  ],
])(
  'safe error %s can recover by refreshing metadata',
  async (status, code, message) => {
    vi.mocked(api.request)
      .mockRejectedValueOnce({
        response: {
          status,
          data: { code, message: 'synthetic-secret-must-not-render' },
        },
      })
      .mockResolvedValueOnce({
        data: { success: true, managed: true, data: metadata },
      })
    mount(42)
    fireEvent.click(screen.getByText('Secure credential'))
    await waitFor(() =>
      expect(screen.getByRole('status')).toHaveTextContent(message)
    )
    expect(document.body.textContent).not.toContain(
      'synthetic-secret-must-not-render'
    )
    expect(screen.queryByLabelText('New API Key')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Refresh metadata' }))
    await screen.findByText('synthetic-reference')
  }
)
