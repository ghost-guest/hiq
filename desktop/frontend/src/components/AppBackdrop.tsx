// AppBackdrop is the optional custom wallpaper layer.
//
// It renders as the first child of the app root so it sits below every other
// positioned sibling, and it is entirely decorative:
//   - aria-hidden, so screen readers never announce it;
//   - pointer-events: none (in CSS), so it can never swallow a click;
//   - unselectable and non-focusable.
//
// The layer reads its state from the --app-bg-* custom properties written by
// lib/wallpaper.ts. When no wallpaper is active this component renders nothing
// at all, leaving the plain themed background exactly as it was before this
// feature existed.
//
// The image is painted as a background rather than an <img> so one layer serves
// cover, contain and tile without extra markup; the scrim above it is the
// legibility control that keeps the UI readable over a photo.

export function AppBackdrop({ active }: { active: boolean }) {
  if (!active) return null;
  return (
    <div className="app-backdrop" aria-hidden="true">
      <div className="app-backdrop__image" />
      <div className="app-backdrop__scrim" />
    </div>
  );
}
