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
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { getAffiliateCode } from '@/features/auth/lib/storage'
import type { SystemStatus } from '@/features/auth/types'
import { api } from '@/lib/api'
import { STATUS_QUERY_KEY } from '@/lib/status-query'
import { useAuthStore } from '@/stores/auth-store'

import { SignUpForm } from '../components/sign-up-form'

let queryClient: QueryClient

function renderSignUp(status: SystemStatus = {}) {
  queryClient.setQueryData(STATUS_QUERY_KEY, {
    registration_invite_mode: 'optional',
    ...status,
  })
  const root = createRootRoute({ component: Outlet })
  const routes = [
    createRoute({
      getParentRoute: () => root,
      path: '/sign-up',
      component: SignUpForm,
    }),
    createRoute({
      getParentRoute: () => root,
      path: '/sign-in',
      component: () => <div>Sign-in page</div>,
    }),
  ]
  const router = createRouter({
    routeTree: root.addChildren(routes),
    history: createMemoryHistory({ initialEntries: ['/sign-up'] }),
  })
  return render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
}

async function fillRegistrationCredentials(
  user: ReturnType<typeof userEvent.setup>
) {
  await user.type(await screen.findByLabelText('Username'), 'new-user')
  await user.type(screen.getByLabelText('Password'), 'Synthetic-password-123')
  await user.type(
    screen.getByLabelText('Confirm password'),
    'Synthetic-password-123'
  )
}

beforeEach(() => {
  window.localStorage.clear()
  window.history.replaceState(null, '', '/sign-up')
  useAuthStore.getState().auth.reset('complete')
  queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
  vi.spyOn(window, 'open').mockImplementation(() => null)
  vi.spyOn(api, 'post').mockResolvedValue({
    data: { success: true, message: '' },
  })
  vi.spyOn(api, 'get').mockRejectedValue(new Error('Unexpected GET request'))
})

afterEach(() => {
  cleanup()
  queryClient.clear()
  vi.restoreAllMocks()
  useAuthStore.getState().auth.reset('idle')
  window.localStorage.clear()
  window.history.replaceState(null, '', '/')
})

it.each(['cache', 'link'] as const)(
  'submits no invitation after the user clears the optional code prefilled from %s',
  async (source) => {
    window.localStorage.setItem('aff', 'CACHED-INVITE')
    if (source === 'link') {
      window.history.replaceState(null, '', '/sign-up?aff=LINK-INVITE')
    }
    renderSignUp()
    const user = userEvent.setup()
    const invitation = await screen.findByLabelText(
      'Invitation code (optional)'
    )
    expect(invitation).toHaveValue(
      source === 'link' ? 'LINK-INVITE' : 'CACHED-INVITE'
    )

    await user.clear(invitation)
    await fillRegistrationCredentials(user)
    await user.click(screen.getByRole('button', { name: 'Create account' }))

    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith(
        '/api/user/register',
        expect.objectContaining({ aff_code: '' }),
        expect.any(Object)
      )
    )
    expect(getAffiliateCode()).toBe('')
  }
)

it.each(['cache', 'link'] as const)(
  'submits the unchanged invitation prefilled from %s even if the cached code changes',
  async (source) => {
    window.localStorage.setItem('aff', 'CACHED-INVITE')
    if (source === 'link') {
      window.history.replaceState(null, '', '/sign-up?aff=LINK-INVITE')
    }
    renderSignUp()
    const user = userEvent.setup()
    const expectedInvitation =
      source === 'link' ? 'LINK-INVITE' : 'CACHED-INVITE'
    expect(
      await screen.findByLabelText('Invitation code (optional)')
    ).toHaveValue(expectedInvitation)
    window.localStorage.setItem('aff', 'OTHER-TAB-INVITE')

    await fillRegistrationCredentials(user)
    await user.click(screen.getByRole('button', { name: 'Create account' }))

    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith(
        '/api/user/register',
        expect.objectContaining({ aff_code: expectedInvitation }),
        expect.any(Object)
      )
    )
    expect(getAffiliateCode()).toBe(expectedInvitation)
  }
)

it('keeps a whitespace-only invitation empty even if another tab changes the cached code', async () => {
  renderSignUp()
  const user = userEvent.setup()
  await user.type(
    await screen.findByLabelText('Invitation code (optional)'),
    '   '
  )
  window.localStorage.setItem('aff', 'OTHER-TAB-INVITE')
  await fillRegistrationCredentials(user)
  await user.click(screen.getByRole('button', { name: 'Create account' }))

  await waitFor(() =>
    expect(api.post).toHaveBeenCalledWith(
      '/api/user/register',
      expect.objectContaining({ aff_code: '' }),
      expect.any(Object)
    )
  )
})

it('submits the trimmed invitation entered by the user instead of the prefilled code', async () => {
  window.localStorage.setItem('aff', 'CACHED-INVITE')
  renderSignUp()
  const user = userEvent.setup()
  const invitation = await screen.findByLabelText('Invitation code (optional)')
  await user.clear(invitation)
  await user.type(invitation, '  NEW-INVITE  ')
  await fillRegistrationCredentials(user)
  await user.click(screen.getByRole('button', { name: 'Create account' }))

  await waitFor(() =>
    expect(api.post).toHaveBeenCalledWith(
      '/api/user/register',
      expect.objectContaining({ aff_code: 'NEW-INVITE' }),
      expect.any(Object)
    )
  )
  expect(getAffiliateCode()).toBe('NEW-INVITE')
})

it('blocks password and OAuth registration when a required invitation is cleared', async () => {
  window.localStorage.setItem('aff', 'CACHED-INVITE')
  renderSignUp({
    registration_invite_mode: 'required',
    github_oauth: true,
    github_client_id: 'test-client',
  })
  const user = userEvent.setup()
  await user.clear(await screen.findByLabelText('Invitation code'))
  await fillRegistrationCredentials(user)
  await user.click(screen.getByRole('button', { name: 'Create account' }))

  expect(await screen.findByText('Invitation code is required')).toBeVisible()
  expect(screen.getByLabelText('Invitation code')).toHaveAttribute(
    'aria-invalid',
    'true'
  )
  expect(
    screen.getByRole('button', { name: /Continue with GitHub/ })
  ).toBeDisabled()
  expect(api.post).not.toHaveBeenCalled()
})

it('omits cached invitations from password registration when invitation mode is hidden', async () => {
  window.localStorage.setItem('aff', 'CACHED-INVITE')
  renderSignUp({ registration_invite_mode: 'hidden' })
  const user = userEvent.setup()
  await fillRegistrationCredentials(user)
  expect(
    screen.queryByPlaceholderText('Enter invitation code')
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Create account' }))

  await waitFor(() =>
    expect(api.post).toHaveBeenCalledWith(
      '/api/user/register',
      expect.objectContaining({ aff_code: undefined }),
      expect.any(Object)
    )
  )
})

it('does not reuse the cleared optional invitation when starting OAuth registration', async () => {
  window.localStorage.setItem('aff', 'CACHED-INVITE')
  renderSignUp({ github_oauth: true, github_client_id: 'test-client' })
  const user = userEvent.setup()
  await user.clear(await screen.findByLabelText('Invitation code (optional)'))
  vi.mocked(api.post)
    .mockResolvedValueOnce({ data: { success: true, message: '' } })
    .mockResolvedValueOnce({
      data: { success: true, data: { flow_token: 'synthetic-oauth-state' } },
    })
  await user.click(screen.getByRole('button', { name: /Continue with GitHub/ }))

  await waitFor(() =>
    expect(api.post).toHaveBeenCalledWith(
      '/api/oauth/state',
      expect.objectContaining({
        provider: 'github',
        intent: 'login',
        aff: undefined,
      }),
      expect.any(Object)
    )
  )
})

it('does not reuse the cleared optional invitation during WeChat registration', async () => {
  window.localStorage.setItem('aff', 'CACHED-INVITE')
  renderSignUp({ wechat_login: true })
  const user = userEvent.setup()
  await user.clear(await screen.findByLabelText('Invitation code (optional)'))
  await user.click(screen.getByRole('button', { name: /Continue with WeChat/ }))
  await user.type(await screen.findByLabelText('Verification code'), '123456')
  vi.mocked(api.get).mockResolvedValue({
    data: { success: false, message: 'Login failed' },
  })
  await user.click(screen.getByRole('button', { name: 'Confirm' }))

  await waitFor(() =>
    expect(api.get).toHaveBeenCalledWith('/api/oauth/wechat', {
      params: { code: '123456', aff: '' },
    })
  )
})
