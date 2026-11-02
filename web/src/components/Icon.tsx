const paths: Record<string, string[]> = {
  grid: ["M3 3h7v7H3z", "M14 3h7v7h-7z", "M3 14h7v7H3z", "M14 14h7v7h-7z"],
  chat: ["M21 12a8 8 0 0 1-8 8H4l2-3a8 8 0 1 1 15-5z"],
  key: ["M14 7a4 4 0 1 0-3.4 3.95L4 17.5V21h3.5l1.1-1.1v-2h2l2-2V14l1.55-1.55A4 4 0 0 0 14 7z"],
  list: ["M8 6h13", "M8 12h13", "M8 18h13", "M3.5 6h.01", "M3.5 12h.01", "M3.5 18h.01"],
  sliders: ["M4 6h10", "M18 6h2", "M4 12h4", "M12 12h8", "M4 18h12", "M20 18h0", "M14 4v4", "M8 10v4", "M16 16v4"],
  logout: ["M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4", "M16 17l5-5-5-5", "M21 12H9"],
  play: ["M7 4l13 8-13 8V4z"],
  stop: ["M6 6h12v12H6z"],
  copy: ["M9 9h11v11H9z", "M5 15H4V4h11v1"],
  plus: ["M12 5v14", "M5 12h14"],
  refresh: ["M20 11a8 8 0 1 0-2.3 6.3", "M20 5v6h-6"],
  bolt: ["M13 2 4 14h6l-1 8 9-12h-6l1-8z"],
  shield: ["M12 3l8 3v6c0 5-3.5 8-8 9-4.5-1-8-4-8-9V6l8-3z"],
  cube: ["M12 2.5 20 7v10l-8 4.5L4 17V7l8-4.5z", "M4 7l8 4.5L20 7", "M12 11.5V21.5"],
  chart: ["M4 20V10", "M10 20V4", "M16 20v-7", "M22 20H2"],
  wallet: ["M3 7a2 2 0 0 1 2-2h13a2 2 0 0 1 2 2v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7z", "M3 9h18", "M16 14h.01"],
  search: ["M11 19a8 8 0 1 0 0-16 8 8 0 0 0 0 16z", "M21 21l-4.3-4.3"],
  download: ["M12 3v12", "M7 10l5 5 5-5", "M4 21h16"],
};

export function Icon({ name, size = 18 }: { name: keyof typeof paths | string; size?: number }) {
  const d = paths[name] || paths.grid;
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
      {d.map((p, i) => (
        <path key={i} d={p} />
      ))}
    </svg>
  );
}
