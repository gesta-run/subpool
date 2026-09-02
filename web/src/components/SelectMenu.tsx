import { useEffect, useId, useLayoutEffect, useRef, useState, type RefObject } from 'react'
import { createPortal } from 'react-dom'
import { CheckIcon, ChevronDownIcon } from './Icons'

export interface SelectMenuOption {
  value: string
  label: string
  disabled?: boolean
}

interface SelectMenuProps {
  id: string
  value: string
  options: SelectMenuOption[]
  onChange: (value: string) => void
  disabled?: boolean
}

interface Position {
  left: number
  top: number
  width: number
}

interface SelectMenuOptionsProps {
  id: string
  listboxID: string
  menuRef: RefObject<HTMLDivElement | null>
  optionRefs: RefObject<Array<HTMLButtonElement | null>>
  options: SelectMenuOption[]
  position: Position
  value: string
  onChoose: (option: SelectMenuOption) => void
  onClose: () => void
  onFocusOption: (start: number, step: number) => void
}

function SelectMenuOptions(props: SelectMenuOptionsProps) {
  return <div id={props.listboxID} ref={props.menuRef} className="select-menu__content" role="listbox" aria-labelledby={props.id} style={{ left: props.position.left, top: props.position.top, width: props.position.width }}>
    {props.options.map((option, index) => <button
      key={option.value}
      ref={(node) => { props.optionRefs.current[index] = node }}
      className="select-menu__option"
      type="button"
      role="option"
      aria-selected={option.value === props.value}
      disabled={option.disabled}
      onClick={() => props.onChoose(option)}
      onKeyDown={(event) => {
        if (event.key === 'ArrowDown') { event.preventDefault(); props.onFocusOption(index, 1) }
        if (event.key === 'ArrowUp') { event.preventDefault(); props.onFocusOption(index, -1) }
        if (event.key === 'Home') { event.preventDefault(); props.optionRefs.current.find((item) => !item?.disabled)?.focus() }
        if (event.key === 'End') { event.preventDefault(); [...props.optionRefs.current].reverse().find((item) => !item?.disabled)?.focus() }
        if (event.key === 'Escape') { event.preventDefault(); props.onClose() }
      }}
    >
      <span>{option.label}</span>
      {option.value === props.value ? <CheckIcon className="select-menu__check" /> : null}
    </button>)}
  </div>
}

export function SelectMenu({ id, value, options, onChange, disabled = false }: SelectMenuProps) {
  const listboxID = useId()
  const rootRef = useRef<HTMLDivElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  const optionRefs = useRef<Array<HTMLButtonElement | null>>([])
  const [open, setOpen] = useState(false)
  const [position, setPosition] = useState<Position>({ left: 0, top: 0, width: 0 })
  const selectedIndex = Math.max(0, options.findIndex((option) => option.value === value))
  const selected = options[selectedIndex]

  function updatePosition() {
    const rect = rootRef.current?.getBoundingClientRect()
    if (!rect) return
    const menuHeight = Math.min(options.length * 44 + 10, 240)
    const spaceBelow = window.innerHeight - rect.bottom - 12
    const placeAbove = spaceBelow < menuHeight && rect.top - 12 > spaceBelow
    setPosition({
      left: rect.left,
      top: placeAbove ? Math.max(12, rect.top - menuHeight - 6) : rect.bottom + 6,
      width: rect.width,
    })
  }

  function openMenu() {
    if (disabled || options.length === 0) return
    updatePosition()
    setOpen(true)
  }

  function closeMenu(returnFocus = false) {
    setOpen(false)
    if (returnFocus) requestAnimationFrame(() => document.getElementById(id)?.focus())
  }

  function choose(option: SelectMenuOption) {
    if (option.disabled) return
    onChange(option.value)
    closeMenu(true)
  }

  function focusOption(start: number, step: number) {
    for (let offset = 1; offset <= options.length; offset += 1) {
      const index = (start + offset * step + options.length) % options.length
      if (!options[index]?.disabled) {
        optionRefs.current[index]?.focus()
        return
      }
    }
  }

  useLayoutEffect(() => {
    if (!open) return
    updatePosition()
    const update = () => updatePosition()
    window.addEventListener('resize', update)
    window.addEventListener('scroll', update, true)
    return () => {
      window.removeEventListener('resize', update)
      window.removeEventListener('scroll', update, true)
    }
  }, [open, options.length])

  useEffect(() => {
    if (!open) return
    requestAnimationFrame(() => optionRefs.current[selectedIndex]?.focus())
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target as Node
      if (!rootRef.current?.contains(target) && !menuRef.current?.contains(target)) closeMenu()
    }
    document.addEventListener('pointerdown', onPointerDown)
    return () => document.removeEventListener('pointerdown', onPointerDown)
  }, [open, selectedIndex])

  return (
    <div className="select-menu" ref={rootRef}>
      <button
        id={id}
        className="select-menu__trigger"
        type="button"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? listboxID : undefined}
        disabled={disabled}
        onClick={() => open ? closeMenu() : openMenu()}
        onKeyDown={(event) => {
          if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
            event.preventDefault()
            openMenu()
          }
        }}
      >
        <span>{selected?.label ?? 'Select an option'}</span>
        <ChevronDownIcon className="select-menu__chevron" />
      </button>
      {open ? createPortal(<SelectMenuOptions id={id} listboxID={listboxID} menuRef={menuRef} optionRefs={optionRefs} options={options} position={position} value={value} onChoose={choose} onClose={() => closeMenu(true)} onFocusOption={focusOption} />, document.body) : null}
    </div>
  )
}
