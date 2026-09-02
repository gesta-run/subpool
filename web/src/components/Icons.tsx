import type { SVGProps } from 'react'

type IconProps = SVGProps<SVGSVGElement>

function Icon({ children, ...props }: IconProps) {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      {...props}
    >
      {children}
    </svg>
  )
}

export const AccountIcon = (props: IconProps) => (
  <Icon {...props}><circle cx="12" cy="8" r="3.5" /><path d="M5.5 20c.5-4 2.7-6 6.5-6s6 2 6.5 6" /></Icon>
)
export const OverviewIcon = (props: IconProps) => (
  <Icon {...props}><path d="M4 20V12h4v8M10 20V4h4v16M16 20V8h4v12" /></Icon>
)
export const PoolIcon = (props: IconProps) => (
  <Icon {...props}><circle cx="6" cy="6" r="2.5" /><circle cx="18" cy="6" r="2.5" /><circle cx="12" cy="18" r="2.5" /><path d="m8 8 2.7 7.4M16 8l-2.7 7.4M8.5 6h7" /></Icon>
)
export const KeyIcon = (props: IconProps) => (
  <Icon {...props}><circle cx="8" cy="15" r="4" /><path d="m11 12 8-8m-2 2 2 2m-5 1 2 2" /></Icon>
)
export const UsageIcon = (props: IconProps) => (
  <Icon {...props}><path d="M4 20V10m5 10V4m6 16v-7m5 7V7" /></Icon>
)
export const SettingsIcon = (props: IconProps) => (
  <Icon {...props}><circle cx="12" cy="12" r="3" /><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.9l.1.1-2.8 2.8-.1-.1a1.7 1.7 0 0 0-1.9-.3 1.7 1.7 0 0 0-1 1.6v.2h-4V21a1.7 1.7 0 0 0-1-1.6 1.7 1.7 0 0 0-1.9.3l-.1.1L4.2 17l.1-.1a1.7 1.7 0 0 0 .3-1.9A1.7 1.7 0 0 0 3 14H2.8v-4H3a1.7 1.7 0 0 0 1.6-1 1.7 1.7 0 0 0-.3-1.9L4.2 7 7 4.2l.1.1A1.7 1.7 0 0 0 9 4.6 1.7 1.7 0 0 0 10 3V2.8h4V3a1.7 1.7 0 0 0 1 1.6 1.7 1.7 0 0 0 1.9-.3l.1-.1L19.8 7l-.1.1a1.7 1.7 0 0 0-.3 1.9 1.7 1.7 0 0 0 1.6 1h.2v4H21a1.7 1.7 0 0 0-1.6 1Z" /></Icon>
)
export const MenuIcon = (props: IconProps) => (
  <Icon {...props}><path d="M4 7h16M4 12h16M4 17h16" /></Icon>
)
export const CloseIcon = (props: IconProps) => (
  <Icon {...props}><path d="m6 6 12 12M18 6 6 18" /></Icon>
)
export const PlusIcon = (props: IconProps) => (
  <Icon {...props}><path d="M12 5v14M5 12h14" /></Icon>
)
export const RefreshIcon = (props: IconProps) => (
  <Icon {...props}><path d="M20 6v5h-5M4 18v-5h5" /><path d="M18.5 10A7 7 0 0 0 6 7.5L4 10m2 4a7 7 0 0 0 12 2.5L20 14" /></Icon>
)
export const CopyIcon = (props: IconProps) => (
  <Icon {...props}><rect x="9" y="9" width="11" height="11" rx="2" /><path d="M15 9V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h3" /></Icon>
)
export const LogoutIcon = (props: IconProps) => (
  <Icon {...props}><path d="M10 5H5v14h5m4-4 4-3-4-3m4 3H9" /></Icon>
)
export const ChevronIcon = (props: IconProps) => (
  <Icon {...props}><path d="m9 6 6 6-6 6" /></Icon>
)
export const ChevronDownIcon = (props: IconProps) => (
  <Icon {...props}><path d="m6 9 6 6 6-6" /></Icon>
)
export const CheckIcon = (props: IconProps) => (
  <Icon {...props}><path d="m5 12 4 4L19 6" /></Icon>
)
export const PowerIcon = (props: IconProps) => (
  <Icon {...props}><path d="M12 3v9" /><path d="M7.1 5.7a8 8 0 1 0 9.8 0" /></Icon>
)
export const TrashIcon = (props: IconProps) => (
  <Icon {...props}><path d="M4 7h16M9 7V4h6v3M7 7l1 13h8l1-13M10 11v5M14 11v5" /></Icon>
)
