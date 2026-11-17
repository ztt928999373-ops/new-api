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
import { useTranslation } from 'react-i18next'

import { useStatus } from '@/hooks/use-status'
import { getSourceRevision } from '@/lib/build-metadata'

export function SourceInfo() {
  const { t } = useTranslation()
  const { status, loading, error, isPlaceholderData } = useStatus()
  const backend = status?.source_revision ?? status?.data?.source_revision
  const frontend = getSourceRevision()
  const matched =
    !loading &&
    !error &&
    !isPlaceholderData &&
    typeof backend === 'string' &&
    /^[0-9a-f]{40}$/.test(backend) &&
    backend === frontend
  if (!matched) {
    return <span>{t('Corresponding source')}: UNKNOWN</span>
  }
  return (
    <span>
      <a
        href={`https://github.com/ztt928999373-ops/new-api/tree/${backend}`}
        target='_blank'
        rel='noopener noreferrer'
        className='underline underline-offset-2'
      >
        {t('Corresponding source')}
      </a>{' '}
      <code className='break-all' lang='en'>
        {backend}
      </code>
    </span>
  )
}
