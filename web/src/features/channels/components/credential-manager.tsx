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
import { zodResolver } from '@hookform/resolvers/zod'
import { useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { z } from 'zod'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { hasPermission } from '@/lib/admin-permissions'
import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

const schema = z.object({
  name: z.string().max(128),
  model: z.string().max(128),
  key: z.string().min(1).max(512).regex(/^\S+$/),
})
type Fields = z.infer<typeof schema>
type Credential = {
  id: string
  status: string
  version: number
  revision: number
  provider: string
}
type Result = {
  success: boolean
  managed?: boolean
  code?: string
  data?: Credential & { id: string }
}

// Only non-secret metadata enters state outside the short-lived password form.
export function CredentialManager(props: { channelId?: number }) {
  const { t } = useTranslation()
  const user = useAuthStore((s) => s.auth.user)
  const canRead = hasPermission(user, 'channel', 'read')
  const canWrite = hasPermission(user, 'channel', 'sensitive_write')
  const canOperate = hasPermission(user, 'channel', 'operate')
  const queryClient = useQueryClient()
  const [open, setOpen] = useState(false)
  const [channelId, setChannelId] = useState(props.channelId)
  const [metadata, setMetadata] = useState<Credential>()
  const [legacy, setLegacy] = useState(false)
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState('')
  const [pendingStatus, setPendingStatus] = useState<string>()
  const form = useForm<Fields>({
    resolver: zodResolver(schema),
    defaultValues: { name: '', model: '', key: '' },
  })

  async function request(
    method: 'get' | 'post' | 'put',
    url: string,
    body?: unknown
  ): Promise<Result> {
    try {
      const response = await api.request<Result>({
        method,
        url,
        data: body,
        skipAuthRefresh: true,
        skipErrorHandler: true,
      })
      if (!response.data.success) throw new Error('unavailable')
      return response.data
    } catch (error) {
      const status = (error as { response?: { status?: number } }).response
        ?.status
      if (status === 403 || status === 401) {
        throw new Error('Credential permission denied')
      }
      if (status === 409) {
        throw new Error('Credential changed. Refresh metadata before retrying.')
      }
      const code = (error as { response?: { data?: { code?: string } } })
        .response?.data?.code
      if (code === 'CHANNEL_CACHE_SYNC_FAILED') {
        throw new Error(
          'Database committed; cache unavailable. Refresh before retrying.'
        )
      }
      throw new Error(
        'Credential operation unavailable. Refresh metadata or contact the operator.'
      )
    }
  }
  async function load(id: number) {
    const result = await request(
      'get',
      `/api/channel/credentials/channel/${id}`
    )
    setLegacy(result.managed === false)
    setMetadata(result.managed ? result.data : undefined)
  }
  async function run(action: () => Promise<void>) {
    if (busy) return
    setBusy(true)
    setMessage('')
    try {
      await action()
      await queryClient.invalidateQueries({ queryKey: ['channels'] })
    } catch (error) {
      setMessage((error as Error).message)
    } finally {
      setBusy(false)
    }
  }
  function changeOpen(value: boolean) {
    if (busy) return
    form.reset()
    setPendingStatus(undefined)
    setMessage('')
    setOpen(value)
    setMetadata(undefined)
    setLegacy(false)
    setChannelId(props.channelId)
    const id = props.channelId
    if (value && id) void run(() => load(id))
  }
  const submit = form.handleSubmit(async (values) => {
    form.resetField('key')
    await run(async () => {
      if (channelId && metadata) {
        await request('put', `/api/channel/credentials/${metadata.id}`, {
          key: values.key,
          revision: metadata.revision,
        })
        await load(channelId)
      } else {
        const result = await request(
          'post',
          '/api/channel/openai-integration',
          values
        )
        const id = Number(result.data?.id)
        setChannelId(id)
        await load(id)
      }
      setMessage(
        'Credential saved. Channel activation is a separate operation.'
      )
    })
  })
  if (!canRead || (!props.channelId && !canWrite)) return null
  return (
    <>
      <Button variant='outline' size='sm' onClick={() => changeOpen(true)}>
        {t(
          props.channelId
            ? 'Secure credential'
            : 'Prepare secure OpenAI channel'
        )}
      </Button>
      <Dialog open={open} onOpenChange={changeOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('Secure credential')}</DialogTitle>
            <DialogDescription>
              {t(
                'Credentials are never displayed. Channel availability and credential status are separate.'
              )}
            </DialogDescription>
          </DialogHeader>
          <p>
            {t(
              'OpenAI connection test checks authentication only, not model availability.'
            )}
          </p>
          {channelId && (
            <p>
              {t('Channel ID')}: {channelId}
            </p>
          )}
          {message && <p role='status'>{t(message)}</p>}
          {legacy && (
            <p role='alert'>
              {t(
                'Legacy channel: secure credential operations are unavailable. No automatic migration.'
              )}
            </p>
          )}
          {metadata && (
            <dl aria-label={t('Credential metadata')}>
              <dt>{t('Provider')}</dt>
              <dd>{metadata.provider}</dd>
              <dt>{t('Status')}</dt>
              <dd>{metadata.status}</dd>
              <dt>{t('Version')}</dt>
              <dd>{metadata.version}</dd>
              <dt>{t('Revision')}</dt>
              <dd>{metadata.revision}</dd>
              <dt>{t('Credential ID')}</dt>
              <dd>{metadata.id}</dd>
            </dl>
          )}
          {metadata && (
            <p>
              {t(
                'Legacy edit, key viewing, copying and channel probes are unsupported for managed channels. Use credential operations here.'
              )}
            </p>
          )}
          {!legacy &&
            canWrite &&
            (!channelId || (metadata && metadata.status !== 'revoked')) && (
              <form onSubmit={submit} autoComplete='off'>
                {!channelId && (
                  <>
                    <Label htmlFor='secure-channel-name'>{t('Name')}</Label>
                    <Input
                      id='secure-channel-name'
                      {...form.register('name', { required: true })}
                      required
                      maxLength={128}
                    />
                    <Label htmlFor='secure-model'>{t('Model')}</Label>
                    <Input
                      id='secure-model'
                      {...form.register('model', { required: true })}
                      required
                      maxLength={128}
                    />
                  </>
                )}
                <Label htmlFor='secure-api-key'>{t('New API Key')}</Label>
                <Input
                  id='secure-api-key'
                  type='password'
                  autoComplete='new-password'
                  {...form.register('key')}
                  required
                  maxLength={512}
                />
                {form.formState.errors.key && (
                  <p role='alert'>
                    {t('Enter a single API Key without whitespace.')}
                  </p>
                )}
                <Button type='submit' disabled={busy}>
                  {t(
                    channelId ? 'Replace credential' : 'Create disabled channel'
                  )}
                </Button>
              </form>
            )}
          {metadata && (
            <div className='flex flex-wrap gap-2'>
              <Button
                disabled={busy || !canOperate || metadata.status !== 'active'}
                onClick={() =>
                  void run(async () => {
                    await request(
                      'post',
                      `/api/channel/credentials/${metadata.id}/test`
                    )
                    setMessage(
                      'Mock or upstream authentication succeeded; model not verified.'
                    )
                  })
                }
              >
                {t('Test credential connection')}
              </Button>
              {canWrite && metadata.status !== 'revoked' && (
                <>
                  <Button
                    disabled={busy}
                    onClick={() =>
                      setPendingStatus(
                        metadata.status === 'active' ? 'disabled' : 'active'
                      )
                    }
                  >
                    {t(
                      metadata.status === 'active'
                        ? 'Disable credential'
                        : 'Enable credential'
                    )}
                  </Button>
                  <Button
                    variant='destructive'
                    disabled={busy}
                    onClick={() => setPendingStatus('revoked')}
                  >
                    {t('Revoke credential')}
                  </Button>
                </>
              )}
            </div>
          )}
          {channelId && (
            <Button
              disabled={busy}
              onClick={() => void run(() => load(channelId))}
            >
              {t('Refresh metadata')}
            </Button>
          )}
        </DialogContent>
      </Dialog>
      <ConfirmDialog
        open={!!pendingStatus}
        onOpenChange={(value) => {
          if (!value && !busy) setPendingStatus(undefined)
        }}
        title={t('Change credential status?')}
        desc={t(
          'Revocation is irreversible. This does not change channel availability.'
        )}
        destructive={pendingStatus === 'revoked'}
        isLoading={busy}
        handleConfirm={() =>
          void run(async () => {
            if (!metadata || !channelId || !canWrite) return
            await request(
              'post',
              `/api/channel/credentials/${metadata.id}/status`,
              { status: pendingStatus, revision: metadata.revision }
            )
            setPendingStatus(undefined)
            await load(channelId)
          })
        }
      />
    </>
  )
}
