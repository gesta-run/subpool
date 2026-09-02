---
version: 0.1
name: Subpool Console Design System
description: A dense self-hosted subscription-pool console aligned with the Gesta operations shell
colors:
  background: "#0A0A0A"
  surface: "#171717"
  nested: "#202020"
  interactive: "#262626"
  text: "#FAFAFA"
  muted: "#A3A3A3"
  border: "#2B2B2B"
  accent: "#6EA8FE"
  success: "#4ADE80"
  warning: "#FBBF24"
  danger: "#F87171"
typography:
  interface: Geist Sans
  data: Geist Mono
rounded:
  control: 7px
  nested: 8px
  panel: 10px
  overlay: 12px
spacing:
  unit: 4px
  panelGap: 16px
  contentGutter: 32px
components:
  sidebarWidth: 228px
  topbarHeight: 60px
  controlHeight: 36px
  panelPadding: 20px
---

## Overview

Subpool is an operational console, not a marketing surface. It inherits Gesta's
dark, neutral shell and information density while making account concurrency,
assignment, routing health, and per-key token usage the primary information.
Color is reserved for status and quantitative charts.

## Colors

- The canvas and sidebar use `background`.
- Route panels and tables use `surface`; inset rows and summaries use `nested`.
- Hover, selected, and expanded states use `interactive`.
- Primary actions use a light neutral fill. Blue is reserved for charts, focus,
  and routing relationships rather than general decoration.
- Every semantic color is paired with an icon, marker, or text label.

## Typography

- Geist Sans is the interface typeface; Aptos and system sans are fallbacks.
- Geist Mono is used for masked keys, model IDs, token totals, and account IDs.
- Page titles are 24–30px at 600 weight. Body copy is 13–14px. Table text is
  12–13px. Compact labels are never smaller than 11px in fixed mockups.
- Sentence case is used throughout. Uppercase is limited to short eyebrows.

## Layout

- Desktop uses a 228px collapsible sidebar, a 60px topbar, and a content gutter
  of 32px. The working area is capped at 1600px.
- Route structure is: title, compact metric strip, primary operational surface,
  then secondary detail or activity.
- At 1024px the sidebar collapses to an icon rail. At 760px it becomes a drawer,
  metric cards stack, and tables use horizontal scrolling with a sticky name
  column.
- Accounts, Pools, API Keys, Usage, and Settings are stable routes. Overview is
  the default landing route.

## Elevation & Depth

- Base: canvas and sidebar.
- Raised: bordered route panels with no shadow.
- Nested: table headers, capacity bands, and detail summaries.
- Overlay: dialogs and drawers with a scrim and a restrained two-layer shadow.
- Focused controls may use a 2px neutral/blue ring; route panels do not glow.

## Shapes

- Route panels use a 10px radius.
- Nested surfaces and tables use an 8px radius.
- Controls use a 7px radius.
- Fully rounded pills are reserved for statuses, providers, and compact filters.
- Visible controls and surfaces never use square corners or a zero radius.

## Components

- `AppShell`: product mark, direct navigation, account footer, breadcrumb, and
  route actions. The active route uses both a filled shape and stronger text.
- `MetricStrip`: four compact metrics with a value, label, and one explanatory
  line. It never becomes a decorative hero.
- `CapacityMeter`: assigned API keys over an account's configured key limit,
  expressed as discrete seats so `2 of 3` is immediately legible.
- `DataTable`: sticky header, searchable/filterable records, row action menu,
  and a detail drawer for editing without leaving context.
- `StatusBadge`: text plus a semantic dot. Required states are healthy, attention,
  disabled, expired, and unavailable.
- `UsagePair`: input and output tokens remain separate. Request or chat content is
  never represented in the console.
- Empty states explain what is missing and provide one next action. Loading uses
  geometry-matched table skeletons. Errors state the cause and offer retry.

## Do's and Don'ts

- Do put OAuth health, request usage, and assignment on the same account row.
- Do make account-to-key assignment inspectable from both Accounts and API Keys.
- Do use clearly illustrative data in design assets and tests.
- Do keep primary actions route-specific: connect account, create pool, create key.
- Don't add organization switching, cross-tenant concepts, or chat-content audit.
- Don't introduce gradients, translucent glass panels, or oversized shadows.
- Don't use square-cornered controls, panels, tables, empty states, or notices.
- Don't confuse employee capacity with request concurrency. The global setting
  limits active employee API keys assigned to each upstream account.

## Architecture and Product Review

- High-confidence requirement: employee key capacity and account health both
  affect account selection.
- Avoid over-engineering: Phase 1 does not need alerts, a command palette, custom
  dashboards, or a generalized workflow builder.
- A smaller version is viable: ship one shell, Overview, Accounts, Pools, API Keys,
  and a read-only Usage route. Use drawers for edit flows before adding deep pages.
