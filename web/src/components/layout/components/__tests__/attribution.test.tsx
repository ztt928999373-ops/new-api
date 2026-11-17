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
import { render, screen, cleanup } from '@testing-library/react'
import i18next from 'i18next'
import type { ReactNode } from 'react'
import { afterEach, describe, expect, test, vi } from 'vitest'

import { About } from '@/features/about'
import en from '@/i18n/locales/en.json'
import fr from '@/i18n/locales/fr.json'
import ja from '@/i18n/locales/ja.json'
import ru from '@/i18n/locales/ru.json'
import viLocale from '@/i18n/locales/vi.json'
import zhTW from '@/i18n/locales/zh-TW.json'
import zh from '@/i18n/locales/zh.json'

import { Footer } from '../footer'

const fixture = vi.hoisted(() => ({
  footerHtml: '',
  about: '',
  backendRevision: '',
  frontendRevision: '',
  placeholder: false,
  fetching: false,
}))
vi.mock('@/hooks/use-system-config', () => ({
  useSystemConfig: () => ({
    systemName: 'Synthetic brand',
    footerHtml: fixture.footerHtml,
  }),
}))
vi.mock('@/hooks/use-status', () => ({
  useStatus: () => ({
    status: { source_revision: fixture.backendRevision },
    isPlaceholderData: fixture.placeholder,
    isFetching: fixture.fetching,
  }),
}))
vi.mock('@tanstack/react-router', () => ({
  Link: (props: { to: string; children: ReactNode; className?: string }) => (
    <a href={props.to} className={props.className}>
      {props.children}
    </a>
  ),
}))
vi.mock('@tanstack/react-query', () => ({
  useQuery: () => ({ data: { data: fixture.about }, isLoading: false }),
}))
vi.mock('@/components/layout', () => ({
  PublicLayout: (props: { children: ReactNode }) => (
    <main>{props.children}</main>
  ),
}))

vi.mock('@/lib/build-metadata', () => ({
  getSourceRevision: () => fixture.frontendRevision,
}))

const notice = 'Frontend design and development by New API contributors.'
const locales = { en, fr, ja, ru, vi: viLocale, zhCN: zh, zhTW }

afterEach(async () => {
  fixture.footerHtml = ''
  fixture.about = ''
  fixture.backendRevision = ''
  fixture.frontendRevision = ''
  fixture.placeholder = false
  fixture.fetching = false
  await i18next.changeLanguage('en')
})

describe('NOTICE attribution', () => {
  test.each(Object.entries(locales))(
    '%s keeps the full English notice with default and custom Footer',
    async (language, resource) => {
      i18next.addResourceBundle(
        language,
        'translation',
        resource.translation,
        true,
        true
      )
      await i18next.changeLanguage(language)
      for (const html of ['', '<p>Synthetic custom footer</p>']) {
        fixture.footerHtml = html
        render(<Footer />)
        expect(screen.getByText(notice, { exact: false })).toBeVisible()
        expect(
          screen
            .getAllByRole('link')
            .some(
              (link) =>
                link.getAttribute('href') ===
                'https://github.com/QuantumNous/new-api'
            )
        ).toBe(true)
        cleanup()
      }
    }
  )

  test.each([
    '',
    'Synthetic **About**',
    '<style>footer { display: none !important }</style><p>Custom About</p>',
    'https://about.example.invalid/',
  ])('About retains the notice outside configured content %s', (about) => {
    fixture.about = about
    render(<About />)
    expect(screen.getByText(notice, { exact: false })).toBeVisible()
    expect(
      screen
        .getAllByRole('link', { name: 'New API' })
        .some(
          (link) =>
            link.getAttribute('href') ===
            'https://github.com/QuantumNous/new-api'
        )
    ).toBe(true)
  })

  test('custom Footer CSS cannot enter the document containing the notice', () => {
    fixture.footerHtml =
      '<style>footer { display:none !important }</style><p>Custom footer</p>'
    const { container } = render(<Footer />)
    expect(container.querySelector('style')).toBeNull()
    expect(screen.getByText(notice, { exact: false })).toBeVisible()
  })
})

describe('corresponding source entry', () => {
  test('a refresh cannot reuse an old matching backend snapshot', () => {
    fixture.backendRevision = 'a'.repeat(40)
    fixture.frontendRevision = fixture.backendRevision
    fixture.fetching = true
    render(<Footer />)
    expect(
      screen.queryByRole('link', { name: 'Corresponding source' })
    ).not.toBeInTheDocument()
    expect(screen.getByText('UNKNOWN', { exact: false })).toBeVisible()
  })

  test('persisted placeholder status cannot assert the running backend identity', () => {
    fixture.backendRevision = 'a'.repeat(40)
    fixture.frontendRevision = fixture.backendRevision
    fixture.placeholder = true
    render(<Footer />)
    expect(
      screen.queryByRole('link', { name: 'Corresponding source' })
    ).not.toBeInTheDocument()
    expect(screen.getByText('UNKNOWN', { exact: false })).toBeVisible()
  })

  test('matching build commits link to the immutable modified source', () => {
    fixture.backendRevision = 'a'.repeat(40)
    fixture.frontendRevision = fixture.backendRevision
    render(<Footer />)
    expect(
      screen.getByRole('link', { name: 'Corresponding source' })
    ).toHaveAttribute(
      'href',
      `https://github.com/ztt928999373-ops/new-api/tree/${fixture.backendRevision}`
    )
    expect(screen.getByText(fixture.backendRevision)).toBeVisible()
  })
  test.each([
    ['', 'a'.repeat(40)],
    ['a'.repeat(40), 'b'.repeat(40)],
    ['a'.repeat(40), 'UNKNOWN'],
  ])(
    'unconfirmed or mismatched builds show UNKNOWN (%s / %s)',
    (backend, frontend) => {
      fixture.backendRevision = backend
      fixture.frontendRevision = frontend
      render(<Footer />)
      expect(
        screen.queryByRole('link', { name: 'Corresponding source' })
      ).not.toBeInTheDocument()
      expect(screen.getByText('UNKNOWN', { exact: false })).toBeVisible()
    }
  )
})
