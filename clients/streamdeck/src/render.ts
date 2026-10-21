import type { KeyView } from "./model.js";

function escape(value: string, max: number): string {
  const clean = value.replace(/[\u0000-\u001f\u007f-\u009f\u202a-\u202e\u2066-\u2069]/g, "");
  const short = [...clean].length > max ? [...clean].slice(0, max - 1).join("") + "…" : clean;
  return short.replace(/[<>&"']/g, (c) => ({ "<": "&lt;", ">": "&gt;", "&": "&amp;", '"': "&quot;", "'": "&apos;" })[c]!);
}

export function renderKey(view: KeyView, icon?: string, pulse = 0, critical = 0): string {
  const meter = view.used !== undefined;
  const color = view.color ?? "#bd93f9";
  const alarm = meter && Number.isFinite(critical) ? Math.max(0, Math.min(1, critical)) : 0;
  const strength = !alarm && meter && Number.isFinite(pulse) ? Math.max(0, Math.min(1, pulse)) : 0;
  const emphasis = 4 * strength + 8 * alarm;
  const alert = alarm > 0 ? '<g data-role="critical-pulse">' +
    '<rect width="144" height="144" rx="18" fill="#ff5555" opacity="' + (.45 * alarm).toFixed(3) + '"/>' +
    '<rect x="4" y="4" width="136" height="136" rx="15" fill="none" stroke="#ff5555" stroke-width="' +
    (2 + 4 * alarm).toFixed(3) + '" opacity="' + (.95 * alarm).toFixed(3) + '"/></g>' : "";
  const radius = 53, circumference = 2 * Math.PI * radius;
  const used = Math.max(0, Math.min(100, view.used ?? 0));
  let ring = '<circle cx="72" cy="72" r="53" fill="none" stroke="#44475a" stroke-width="12"/>';
  if (strength > 0) {
    ring = '<circle data-role="activity-pulse" cx="72" cy="72" r="' + (61 + 5 * strength).toFixed(3) +
      '" fill="none" stroke="' + color + '" stroke-width="' + (2 + 3 * strength).toFixed(3) +
      '" opacity="' + (.35 * strength).toFixed(3) + '"/>' + ring;
  }
  // Zero stays empty; 100 uses a complete circle without a cap seam.
  if (meter && used > 0) {
    ring += '<circle data-role="usage" cx="72" cy="72" r="53" fill="none" stroke="' + color +
      '" stroke-width="' + (12 + emphasis).toFixed(3) + '" stroke-linecap="round" transform="rotate(-90 72 72)"' +
      (used < 100 ? ' stroke-dasharray="' + used / 100 * circumference + ' ' + circumference + '"' : '') + '/>';
  }
  if (meter && view.expected !== undefined) {
    const angle = (view.expected / 100 * 360 - 90) * Math.PI / 180;
    const point = (distance: number) => [(72 + Math.cos(angle) * distance).toFixed(3), (72 + Math.sin(angle) * distance).toFixed(3)];
    const [x1, y1] = point(43), [x2, y2] = point(66);
    const line = ' x1="' + x1 + '" y1="' + y1 + '" x2="' + x2 + '" y2="' + y2 + '"';
    ring += '<g data-role="pace-marker"><line' + line + ' stroke="#282a36" stroke-width="9"/>' +
      '<line' + line + ' stroke="#f8f8f2" stroke-width="5"/></g>';
  }
  if (icon && /^data:image\/png;base64,[A-Za-z0-9+/=]+$/.test(icon)) {
    ring += meter
      ? '<image x="42" y="42" width="60" height="60" href="' + icon + '"/>'
      : '<image x="50" y="38" width="44" height="44" href="' + icon + '"/>';
  }
  // Normal keys are entirely graphical. Only unavailable states need text.
  if (!meter) {
    ring += '<text x="72" y="104" text-anchor="middle" font-family="Arial,Helvetica,sans-serif" ' +
      'font-size="15" fill="#b8b8c6">' + escape(view.value, 12) + '</text>';
  } else if (view.expected === undefined) {
    ring += '<g data-role="pace-unavailable"><circle cx="116" cy="116" r="13" fill="#6272a4" stroke="#282a36" stroke-width="5"/>' +
      '<text x="116" y="122" text-anchor="middle" font-family="Arial,Helvetica,sans-serif" font-size="18" font-weight="bold" fill="#f8f8f2">?</text></g>';
  }
  // Preserve meter details in the SVG title.
  const description = [view.provider, view.label, view.value, meter ? view.display ?? "used" : "",
    view.pace ?? "", view.detail].filter(Boolean).join(" · ");
  return '<svg xmlns="http://www.w3.org/2000/svg" width="144" height="144" viewBox="0 0 144 144">' +
    '<title>' + escape(description, 200) + '</title>' +
    '<rect width="144" height="144" rx="18" fill="#282a36"/>' + alert + ring + '</svg>';
}
export function keyImage(view: KeyView, icon?: string, pulse = 0, critical = 0): string {
  return "data:image/svg+xml;base64," + Buffer.from(renderKey(view, icon, pulse, critical)).toString("base64");
}
