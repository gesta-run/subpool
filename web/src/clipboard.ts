export async function copyText(value: string) {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(value)
      return
    } catch {
      // Fall back for embedded browsers that expose the API but deny permission.
    }
  }

  const input = document.createElement('textarea')
  const activeElement = document.activeElement instanceof HTMLElement ? document.activeElement : null
  input.value = value
  input.readOnly = true
  input.tabIndex = -1
  input.setAttribute('aria-hidden', 'true')
  input.style.position = 'fixed'
  input.style.opacity = '0'
  document.body.appendChild(input)
  input.select()
  const copied = document.execCommand('copy')
  input.remove()
  activeElement?.focus()
  if (!copied) throw new Error('Clipboard access was denied')
}
