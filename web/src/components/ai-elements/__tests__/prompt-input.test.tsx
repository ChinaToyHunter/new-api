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
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import {
  PromptInput,
  PromptInputProvider,
  PromptInputSubmit,
  PromptInputTextarea,
  usePromptInputAttachments,
  type PromptInputMessage,
} from '../prompt-input'

function AttachmentList() {
  const attachments = usePromptInputAttachments()
  return (
    <ul aria-label='Attachments'>
      {attachments.files.map((file) => (
        <li key={file.id}>{file.filename}</li>
      ))}
    </ul>
  )
}

beforeEach(() => {
  vi.stubGlobal(
    'URL',
    class extends URL {
      static createObjectURL = vi.fn((file: File) => `blob:${file.name}`)
      static revokeObjectURL = vi.fn()
    }
  )
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue({
      blob: async () => new Blob(['image'], { type: 'image/png' }),
    })
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
})

it.each(['local', 'provider'] as const)(
  'appends uploads in order and removes the last attachment on Backspace in %s mode',
  async (mode) => {
    const user = userEvent.setup()
    const input = (
      <PromptInput multiple onSubmit={vi.fn()}>
        <PromptInputTextarea aria-label='Message' />
        <AttachmentList />
      </PromptInput>
    )
    render(
      mode === 'provider' ? (
        <PromptInputProvider>{input}</PromptInputProvider>
      ) : (
        input
      )
    )
    const upload = screen.getByLabelText('Upload files')
    await user.upload(
      upload,
      new File(['one'], 'one.png', { type: 'image/png' })
    )
    await user.upload(
      upload,
      new File(['two'], 'two.png', { type: 'image/png' })
    )
    expect(
      screen.getAllByRole('listitem').map((item) => item.textContent)
    ).toEqual(['one.png', 'two.png'])

    await user.click(screen.getByRole('textbox', { name: 'Message' }))
    await user.keyboard('{Backspace}')
    expect(screen.getByRole('listitem')).toHaveTextContent('one.png')
    expect(screen.queryByText('two.png')).not.toBeInTheDocument()
  }
)

it.each(['success', 'rejected'] as const)(
  'converts attachments to Base64 and preserves the %s submission cleanup contract',
  async (outcome) => {
    const user = userEvent.setup()
    const onSubmit = vi.fn(async (_message: PromptInputMessage) => {
      if (outcome === 'rejected') throw new Error('Submission failed')
    })
    render(
      <PromptInputProvider initialInput='Describe the image'>
        <PromptInput onSubmit={onSubmit}>
          <PromptInputTextarea aria-label='Message' />
          <AttachmentList />
          <PromptInputSubmit />
        </PromptInput>
      </PromptInputProvider>
    )
    await user.upload(
      screen.getByLabelText('Upload files'),
      new File(['image'], 'image.png', { type: 'image/png' })
    )
    await user.click(screen.getByRole('button', { name: 'Submit' }))
    await waitFor(() => expect(onSubmit).toHaveBeenCalledOnce())
    expect(onSubmit.mock.calls[0][0]).toEqual({
      text: 'Describe the image',
      files: [
        {
          type: 'file',
          filename: 'image.png',
          mediaType: 'image/png',
          url: 'data:image/png;base64,aW1hZ2U=',
        },
      ],
    })
    if (outcome === 'success') {
      await waitFor(() =>
        expect(screen.queryByRole('listitem')).not.toBeInTheDocument()
      )
      expect(screen.getByRole('textbox', { name: 'Message' })).toHaveValue('')
    } else {
      expect(screen.getByRole('listitem')).toHaveTextContent('image.png')
      expect(screen.getByRole('textbox', { name: 'Message' })).toHaveValue(
        'Describe the image'
      )
    }
  }
)
