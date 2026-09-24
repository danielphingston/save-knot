---
version: alpha
name: SaveKnot
description: A local-first game save monitor with precise backup telemetry and calm desktop controls.
colors:
  primary: "#4ade80"
  background: "#0a0c0e"
  surface: "#0f1318"
typography:
  sans:
    fontFamily: "Geist, Inter, ui-sans-serif, system-ui, sans-serif"
  mono:
    fontFamily: "JetBrains Mono, ui-monospace, SFMono-Regular, Consolas, monospace"
rounded:
  control: "8px"
  card: "12px"
  panel: "16px"
spacing:
  page-gutter: "clamp(24px, 4.6vw, 68px)"
  section-gap: "38px"
  control-gap: "8px"
components:
  button:
    backgroundColor: "#4ade80"
    textColor: "#0a0c0e"
    rounded: "8px"
  game-card:
    backgroundColor: "#0f1318"
    textColor: "#f2f5f8"
    rounded: "12px"
  panel:
    backgroundColor: "#0f1318"
    textColor: "#f2f5f8"
    rounded: "16px"
  field:
    backgroundColor: "#0a0c0e"
    textColor: "#f2f5f8"
    rounded: "8px"
---

# SaveKnot Design System

## Overview

### Creative North Star

The UI feels like a well-made save monitor in a quiet control room: stable surfaces, compact telemetry, and a single live accent. It keeps Stitch's dark cyber-precision direction but softens the card corners and surface contrast for long desktop sessions.

### Product context and register

- **Audience and primary job:** Desktop players managing local snapshots and optional Cloudflare R2 sync.
- **Target market(s) and evidence:** English-language desktop utility; no region-specific workflow is established in the repository.
- **Locale(s) and language policy:** Browser locale formats dates; interface copy is English.
- **Usage scene:** Desktop dashboard used to inspect game saves, restore versions, and adjust background tasks.
- **Register:** Product utility across Games, Game Details, Activity, and Settings.
- **Memorable signature:** Consistent cover panels, including generated initials plates when artwork is unavailable.
- **Restraint:** Status, path, time, and snapshot content remain easy to scan; glow is reserved for selected/live states.
- **Anti-references:** Boxy generic admin grids, oversized placeholder initials, and pill-shaped controls everywhere.
- **Token ownership/runtime mapping:** `internal/api/web/styles.css` is the canonical runtime token source. This file mirrors it. `--accent` maps to primary actions, focus, and selected state; `--bg`, `--panel`, `--panel-raised`, `--line`, `--text`, and `--muted` map to surfaces and type. Radius tokens map to `--radius-control`, `--radius-card`, and `--radius-panel`. Accent overrides under `:root[data-accent]` adapt those semantic runtime variables for Lime, Violet, and Coral. Review token drift with `make ui-check` and rendered desktop inspection.

## Colors

The near-black background (`#0a0c0e`), panel (`#0f1318`), and raised surface (`#151a21`) keep attention on saved data. Foreground and secondary text use `#f2f5f8` and `#9aa8b6`; borders use a 9% white-blue hairline. The default telemetry green (`#4ade80`) marks primary actions, selected navigation, live state, and focus. Violet (`#b9a6ff`) and Coral (`#ff907d`) are user-selected accent themes. Warning amber (`#fbbf24`) and destructive rose (`#fb7185`) keep their meanings in every theme. Scrollbars use `#35404a` on the near-black track.

## Typography

Use Geist with Inter and system fallbacks for interface text. Use JetBrains Mono with platform monospace fallbacks for technical labels, timestamps, paths, hashes, and metrics. Display titles are compact and semibold; forms and data keep open line height and readable 11–14px metadata.

## Layout

A stable 248px left rail anchors three destinations. A slim status bar sits over a broad document-scrolling workspace. Library controls and summary precede a bounded, paginated grid. Detail content uses a summary header and ordered backup history; Settings uses vertically stacked forms. Desktop widths are the product target. Keep the existing narrow-window fallback usable without adding a mobile-specific surface.

## Elevation & Depth

Use tonal panels, subtle 1px borders, and modest card shadows. No blur layers are needed except the cover badge over real artwork. Active and live states may carry a restrained accent halo.

## Shapes

Use 8px controls, 12px cards, and 16px major panels. Navigation selection uses a narrow accent edge. Badges are compact and squared rather than pill-shaped.

## Components

### Foundational visual states

Hover changes border and background within 120–180ms. Focus remains visibly outlined. Busy controls keep their dimensions; disabled controls lower contrast. Warning, destructive, success, and accent states combine text and color.

### Buttons and actions

Primary is solid accent; secondary is a raised outline; destructive actions stay visually quiet until their existing confirmation guard.

### Navigation and data display

Game cards share one cover geometry. Missing artwork uses a framed initials plate on a low-contrast technical pattern. Snapshot history, file changes, and activity keep time, size, and paths aligned and scannable.

### Forms and overlays

Native form fields retain familiar keyboard behavior with shared dark surfaces and focus rings. Existing app dialogs and toast regions keep their placement and hierarchy.

### Iconography

Retain the small existing Unicode navigation symbols and provide text labels alongside them. Do not introduce icons without a clear operational role.

### Motion

Use 120–180ms control feedback, a subtle 2.5s watcher pulse, a short fade/vertical shift between routes, and a named cover transition for a clicked game card. Do not animate unrelated data updates. Respect `prefers-reduced-motion`.

### Content and data visualization

Use direct action verbs and preserve game, snapshot, time, and sync terminology. Numeric values use tabular alignment where available.

## Do's and Don'ts

- **Do:** Keep each status attached to the game or backup it describes.
- **Do:** Use the selected accent consistently for action and focus.
- **Don't:** Let missing artwork create a different card layout.
- **Don't:** use accent color as the only signal for warning or destructive state.
