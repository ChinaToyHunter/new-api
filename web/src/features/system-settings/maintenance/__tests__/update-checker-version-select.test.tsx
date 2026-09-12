/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.
*/
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, test, vi } from 'vitest'

import * as updateApi from '../../api'
import { UpdateCheckerSection } from '../update-checker-section'

vi.mock('../../api', () => ({
  checkSystemUpdate: vi.fn(),
  listSystemUpdateReleases: vi.fn(),
  performSystemUpdate: vi.fn(),
  restartSystem: vi.fn(),
}))

vi.mock('sonner', () => ({
  toast: {
    error: vi.fn(),
    message: vi.fn(),
    success: vi.fn(),
    warning: vi.fn(),
  },
}))

const checkResponse = {
  success: true,
  message: '',
  data: {
    deploy_mode: 'docker' as const,
    current_version: 'v2.0.0',
    latest_version: 'v3.0.0',
    has_update: true,
    docker: { socket_available: true },
    update_source: 'ChinaToyHunter/new-api',
    enabled: true,
    cached: false,
  },
}

describe('system update version selection', () => {
  beforeEach(() => {
    vi.mocked(updateApi.checkSystemUpdate).mockResolvedValue(checkResponse)
    vi.mocked(updateApi.listSystemUpdateReleases).mockResolvedValue({
      success: true,
      message: '',
      data: [
        { tag_name: 'v3.0.0', name: 'latest' },
        { tag_name: 'v1.0.0', name: 'rollback' },
      ],
    })
    vi.mocked(updateApi.performSystemUpdate).mockResolvedValue({
      success: true,
      message: '',
      data: {
        message: 'scheduled',
        need_restart: false,
        deploy_mode: 'docker',
        from_version: 'v2.0.0',
        to_version: 'v1.0.0',
      },
    })
  })

  test('does not load or show release selection outside test mode', () => {
    render(
      <UpdateCheckerSection currentVersion='v2.0.0' testModeEnabled={false} />
    )

    expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
    expect(updateApi.listSystemUpdateReleases).not.toHaveBeenCalled()
  })

  test('loads releases and submits the selected version', async () => {
    const user = userEvent.setup()
    render(<UpdateCheckerSection currentVersion='v2.0.0' testModeEnabled />)

    // Real admin flow: check for updates first so deploy mode is known.
    await user.click(
      await screen.findByRole('button', { name: 'Check for updates' })
    )
    await waitFor(() => {
      expect(updateApi.checkSystemUpdate).toHaveBeenCalled()
    })

    const versionSelect = await screen.findByRole('combobox', {
      name: 'Target version',
    })
    await user.click(versionSelect)
    await user.click(await screen.findByRole('option', { name: /v1\.0\.0/ }))
    await user.click(screen.getByRole('button', { name: 'Replace version' }))

    expect(
      await screen.findByText(
        'Replace v2.0.0 with v1.0.0 using docker deployment?'
      )
    ).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Replace version' }))
    await waitFor(() => {
      expect(updateApi.performSystemUpdate).toHaveBeenCalledWith('v1.0.0')
    })
  })

  test('shows a recoverable error when release loading fails', async () => {
    vi.mocked(updateApi.listSystemUpdateReleases).mockRejectedValueOnce(
      new Error('network unavailable')
    )
    render(<UpdateCheckerSection currentVersion='v2.0.0' testModeEnabled />)

    expect(
      await screen.findByText('Failed to load available versions.')
    ).toBeInTheDocument()
    const retry = screen.getByRole('button', { name: 'Retry' })
    fireEvent.click(retry)

    await waitFor(() => {
      expect(updateApi.listSystemUpdateReleases).toHaveBeenCalledTimes(2)
    })
  })
})
