interface BrandCreditProps {
  className?: string
}

export function BrandCredit({ className = '' }: BrandCreditProps) {
  return (
    <a className={`sidebar__credit ${className}`.trim()} href="https://ubang.cn/ubang/home" target="_blank" rel="noreferrer" aria-label="Supported by Ubang">
      <span>Supported by</span>
      <i aria-hidden="true" />
      <img src="/brand/ubang-logo-light.png" alt="" />
    </a>
  )
}
