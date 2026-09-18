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
import {
  ArrowDownToLineIcon,
  ExternalLinkIcon,
  RefreshCcwIcon,
} from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Markdown } from '@/components/ui/markdown'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { SystemUpdateAction } from '@/features/system-update/system-update-action'
import { formatTimestamp, formatTimestampToDate } from '@/lib/format'

import {
  checkSystemUpdate,
  listSystemUpdateReleases,
  performSystemUpdate,
  restartSystem,
} from '../api'
import { SettingsSection } from '../components/settings-section'
import type { SystemUpdateCheckData, SystemUpdateReleaseInfo } from '../types'

type UpdateCheckerSectionProps = {
  currentVersion?: string | null
  startTime?: number | null
  testModeEnabled?: boolean
}

async function waitForServiceReady(timeoutMs = 120_000) {
  const started = Date.now()
  while (Date.now() - started < timeoutMs) {
    try {
      const res = await fetch('/api/status', { credentials: 'include' })
      if (res.ok) {
        return true
      }
    } catch {
      // service still restarting
    }
    await new Promise((resolve) => setTimeout(resolve, 2000))
  }
  return false
}

export function UpdateCheckerSection({
  currentVersion,
  startTime,
  testModeEnabled = false,
}: UpdateCheckerSectionProps) {
  const { t } = useTranslation()
  const [checking, setChecking] = useState(false)
  const [pulling, setPulling] = useState(false)
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [dialogOpen, setDialogOpen] = useState(false)
  const [release, setRelease] = useState<SystemUpdateReleaseInfo | null>(null)
  const [releases, setReleases] = useState<SystemUpdateReleaseInfo[]>([])
  const [versionsLoading, setVersionsLoading] = useState(false)
  const [versionsError, setVersionsError] = useState(false)
  const [selectedVersion, setSelectedVersion] = useState<string | null>(null)
  const [checkInfo, setCheckInfo] = useState<SystemUpdateCheckData | null>(null)
  const [displayVersion, setDisplayVersion] = useState(currentVersion || '')

  const uptime = startTime ? formatTimestamp(startTime) : t('Unknown')
  const version = displayVersion || currentVersion || t('Unknown')
  const targetVersion = testModeEnabled
    ? selectedVersion
    : checkInfo?.latest_version

  const loadReleases = useCallback(async () => {
    setVersionsLoading(true)
    setVersionsError(false)
    try {
      const body = await listSystemUpdateReleases()
      if (!body.success) {
        throw new Error(body.message || t('Failed to load available versions.'))
      }
      const availableReleases = body.data || []
      setReleases(availableReleases)
      setSelectedVersion((current) =>
        current && availableReleases.some((item) => item.tag_name === current)
          ? current
          : availableReleases[0]?.tag_name || null
      )
    } catch {
      setReleases([])
      setSelectedVersion(null)
      setVersionsError(true)
    } finally {
      setVersionsLoading(false)
    }
  }, [t])

  useEffect(() => {
    if (testModeEnabled) {
      void loadReleases()
    } else {
      setReleases([])
      setSelectedVersion(null)
      setVersionsError(false)
    }
  }, [loadReleases, testModeEnabled])

  const socketBlocked =
    checkInfo?.deploy_mode === 'docker' &&
    checkInfo.docker?.socket_available === false
  const pullDisabled =
    checking ||
    pulling ||
    versionsLoading ||
    checkInfo?.enabled === false ||
    socketBlocked ||
    (testModeEnabled && !selectedVersion)

  const handleCheckUpdates = useCallback(async () => {
    setChecking(true)
    try {
      const body = await checkSystemUpdate(true)
      if (!body.success) {
        throw new Error(body.message || t('Failed to check for updates'))
      }
      const data = body.data
      setCheckInfo(data)
      if (data.current_version) {
        setDisplayVersion(data.current_version)
      }
      if (data.warning) {
        toast.warning(data.warning)
      }
      if (!data.has_update) {
        toast.success(
          t('Already up to date. No update needed ({{version}}).', {
            version: data.latest_version || data.current_version,
          })
        )
        return
      }
      if (data.release_info) {
        setRelease(data.release_info)
        setDialogOpen(true)
      } else {
        toast.message(
          t('New version available: {{version}}', {
            version: data.latest_version,
          })
        )
      }
    } catch (error) {
      const message =
        error instanceof Error ? error.message : t('Failed to check for updates')
      toast.error(message)
    } finally {
      setChecking(false)
    }
  }, [t])

  const handlePullClick = async () => {
    if (pulling) return
    if (testModeEnabled) {
      if (!selectedVersion) {
        toast.error(t('Select a target version.'))
        return
      }
      if (selectedVersion === version) {
        toast.success(
          t('Selected version is already installed ({{version}}).', {
            version: selectedVersion,
          })
        )
        return
      }
      setCheckInfo((current) => ({
        deploy_mode: current?.deploy_mode || 'binary',
        current_version: version,
        latest_version: selectedVersion,
        has_update: true,
        docker: current?.docker,
        binary: current?.binary,
        update_source: current?.update_source || 'ChinaToyHunter/new-api',
        enabled: current?.enabled ?? true,
        cached: false,
      }))
      setConfirmOpen(true)
      return
    }

    setChecking(true)
    try {
      const body = await checkSystemUpdate(true)
      if (!body.success) {
        throw new Error(body.message || t('Failed to check for updates'))
      }
      const data = body.data
      setCheckInfo(data)
      if (!data.enabled) {
        toast.error(t('Self-update is disabled.'))
        return
      }
      if (
        data.deploy_mode === 'docker' &&
        data.docker?.socket_available === false
      ) {
        toast.error(
          t(
            'Docker socket unavailable. Mount /var/run/docker.sock to enable one-click updates.'
          )
        )
        return
      }
      if (!data.has_update) {
        toast.success(
          t('Already up to date. No update needed ({{version}}).', {
            version: data.latest_version || data.current_version,
          })
        )
        return
      }
      setConfirmOpen(true)
    } catch (error) {
      const message =
        error instanceof Error ? error.message : t('Failed to check for updates')
      toast.error(message)
    } finally {
      setChecking(false)
    }
  }

  const handleConfirmPull = async () => {
    const versionToInstall = testModeEnabled
      ? selectedVersion || undefined
      : undefined
    if (testModeEnabled && !versionToInstall) {
      toast.error(t('Select a target version.'))
      return
    }
    setConfirmOpen(false)
    setPulling(true)
    try {
      const body = await performSystemUpdate(versionToInstall)
      if (!body.success) {
        throw new Error(body.message || t('Update failed'))
      }
      const data = body.data
      if (data.already_up_to_date) {
        toast.success(
          t('You are running the latest version ({{version}}).', {
            version: data.to_version || data.from_version,
          })
        )
        return
      }

      if (data.deploy_mode === 'binary' && data.need_restart) {
        toast.success(t('Update completed. Restarting...'))
        try {
          await restartSystem()
        } catch {
          toast.message(
            t('Update completed. Please restart the service manually.')
          )
        }
        toast.message(t('Waiting for service to come back...'))
        const ready = await waitForServiceReady()
        if (ready) {
          toast.success(t('Update completed.'))
          window.location.reload()
        } else {
          toast.error(t('Service did not come back in time. Refresh manually.'))
        }
        return
      }

      // Docker recreate tears down this process; poll until back.
      toast.success(t('Update completed.'))
      toast.message(t('Waiting for service to come back...'))
      const ready = await waitForServiceReady()
      if (ready) {
        window.location.reload()
      } else {
        toast.error(t('Service did not come back in time. Refresh manually.'))
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : t('Update failed')
      toast.error(message)
    } finally {
      setPulling(false)
    }
  }

  const goToRelease = () => {
    if (release?.html_url) {
      window.open(release.html_url, '_blank', 'noopener,noreferrer')
    }
  }

  const buildConfirmDescription = () => {
    if (!checkInfo || !targetVersion) return undefined
    return testModeEnabled
      ? t('Replace {{from}} with {{to}} using {{mode}} deployment?', {
          from: checkInfo.current_version,
          to: targetVersion,
          mode: checkInfo.deploy_mode,
        })
      : t('Update from {{from}} to {{to}} ({{mode}})?', {
          from: checkInfo.current_version,
          to: targetVersion,
          mode: checkInfo.deploy_mode,
        })
  }

  return (
    <>
      <SettingsSection title={t('System maintenance')}>
        <div className='space-y-6'>
          <div className='grid gap-4 md:grid-cols-2'>
            <div className='rounded-lg border p-4'>
              <div className='text-muted-foreground text-sm'>
                {t('Current version')}
              </div>
              <div className='text-lg font-semibold break-all'>{version}</div>
            </div>
            <div className='rounded-lg border p-4'>
              <div className='text-muted-foreground text-sm'>
                {t('Uptime since')}
              </div>
              <div className='text-lg font-semibold'>{uptime}</div>
            </div>
          </div>

          <SystemUpdateAction compact={false} />

          <div className='flex flex-wrap items-center gap-3'>
            <Button onClick={handleCheckUpdates} disabled={checking || pulling}>
              {checking ? (
                t('Checking updates...')
              ) : (
                <>
                  <RefreshCcwIcon className='me-2 h-4 w-4' />
                  {t('Check for updates')}
                </>
              )}
            </Button>
            {testModeEnabled && (
              <Select
                value={selectedVersion ?? undefined}
                onValueChange={setSelectedVersion}
                disabled={versionsLoading || pulling || releases.length === 0}
              >
                <SelectTrigger
                  aria-label={t('Target version')}
                  className='max-w-full min-w-44'
                >
                  <SelectValue
                    placeholder={
                      versionsLoading
                        ? t('Loading versions...')
                        : t('Select a target version')
                    }
                  />
                </SelectTrigger>
                <SelectContent align='start'>
                  {releases.map((item) => (
                    <SelectItem key={item.tag_name} value={item.tag_name}>
                      {item.tag_name}
                      {item.name && item.name !== item.tag_name
                        ? ` · ${item.name}`
                        : ''}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
            <Button
              variant='default'
              onClick={handlePullClick}
              disabled={pullDisabled}
            >
              {pulling ? (
                t('Pulling update...')
              ) : (
                <>
                  <ArrowDownToLineIcon className='me-2 h-4 w-4' />
                  {testModeEnabled ? t('Replace version') : t('Pull update')}
                </>
              )}
            </Button>
          </div>

          {testModeEnabled && versionsError && (
            <div className='flex flex-wrap items-center gap-2' role='alert'>
              <p className='text-destructive text-sm'>
                {t('Failed to load available versions.')}
              </p>
              <Button
                type='button'
                variant='outline'
                size='sm'
                onClick={() => void loadReleases()}
                disabled={versionsLoading || pulling}
              >
                {t('Retry')}
              </Button>
            </div>
          )}
          {testModeEnabled &&
            !versionsLoading &&
            !versionsError &&
            releases.length === 0 && (
              <p className='text-muted-foreground text-sm'>
                {t('No available releases were found.')}
              </p>
            )}
          {socketBlocked && (
            <p className='text-muted-foreground text-sm'>
              {t(
                'Docker socket unavailable. Mount /var/run/docker.sock to enable one-click updates.'
              )}
            </p>
          )}
          {checkInfo?.enabled === false && (
            <p className='text-muted-foreground text-sm'>
              {t('Self-update is disabled.')}
            </p>
          )}
          <p className='text-muted-foreground text-xs'>
            {t('Deploy source: {{repo}}', {
              repo: checkInfo?.update_source || 'ChinaToyHunter/new-api',
            })}
          </p>

        </div>
      </SettingsSection>

      <Dialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={
          testModeEnabled
            ? t('Confirm version replacement')
            : t('Confirm update')
        }
        description={
          checkInfo && targetVersion ? buildConfirmDescription() : undefined
        }
        contentHeight='auto'
        footer={
          <>
            <Button
              type='button'
              variant='secondary'
              onClick={() => setConfirmOpen(false)}
            >
              {t('Close')}
            </Button>
            <Button
              type='button'
              onClick={handleConfirmPull}
              disabled={pulling}
            >
              {testModeEnabled ? t('Replace version') : t('Pull update')}
            </Button>
          </>
        }
      >
        <p className='text-muted-foreground text-sm'>
          {t('Deploy source: {{repo}}', {
            repo: checkInfo?.update_source || 'ChinaToyHunter/new-api',
          })}
        </p>
      </Dialog>

      <Dialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        title={
          release?.tag_name
            ? t('New version available: {{version}}', {
                version: release.tag_name,
              })
            : t('Release details')
        }
        description={
          release?.published_at
            ? `${t('Published')} ${formatTimestampToDate(
                new Date(release.published_at).getTime(),
                'milliseconds'
              )}`
            : undefined
        }
        contentClassName='max-h-[80vh] overflow-y-auto'
        contentHeight='auto'
        bodyClassName='space-y-4'
        footer={
          <>
            <Button
              type='button'
              variant='secondary'
              onClick={() => setDialogOpen(false)}
            >
              {t('Close')}
            </Button>
            {release?.html_url && (
              <Button type='button' onClick={goToRelease}>
                <ExternalLinkIcon className='me-2 h-4 w-4' />
                {t('Open release')}
              </Button>
            )}
            <Button
              type='button'
              onClick={() => {
                setDialogOpen(false)
                setConfirmOpen(true)
              }}
            >
              <ArrowDownToLineIcon className='me-2 h-4 w-4' />
              {testModeEnabled ? t('Replace version') : t('Pull update')}
            </Button>
          </>
        }
      >
        <div className='space-y-4'>
          {release?.body ? (
            <Markdown>{release.body}</Markdown>
          ) : (
            <p className='text-muted-foreground text-sm'>
              {t('No release notes provided.')}
            </p>
          )}
        </div>
      </Dialog>
    </>
  )
}
