import { afterEach, describe, expect, it, vi } from 'vitest'
import { copyText } from './clipboard'

describe('copyText', () => {
  const clipboardDescriptor = Object.getOwnPropertyDescriptor(navigator, 'clipboard')
  const execCommandDescriptor = Object.getOwnPropertyDescriptor(document, 'execCommand')

  afterEach(() => {
    if (clipboardDescriptor) Object.defineProperty(navigator, 'clipboard', clipboardDescriptor)
    else Reflect.deleteProperty(navigator, 'clipboard')
    if (execCommandDescriptor) Object.defineProperty(document, 'execCommand', execCommandDescriptor)
    else Reflect.deleteProperty(document, 'execCommand')
    document.body.replaceChildren()
  })

  it('falls back when clipboard permission is denied and restores focus', async () => {
    const writeText = vi.fn().mockRejectedValue(new Error('denied'))
    const execCommand = vi.fn().mockReturnValue(true)
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } })
    Object.defineProperty(document, 'execCommand', { configurable: true, value: execCommand })
    const button = document.createElement('button')
    document.body.appendChild(button)
    button.focus()

    await copyText('sk-example-secret')

    expect(writeText).toHaveBeenCalledWith('sk-example-secret')
    expect(execCommand).toHaveBeenCalledWith('copy')
    expect(button).toHaveFocus()
    expect(document.querySelector('textarea')).not.toBeInTheDocument()
  })
})
