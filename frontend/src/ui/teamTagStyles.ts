import type { TeamTagStyle } from "../api/generated";

const tagStyleClasses: Record<TeamTagStyle, string> = {
  indigo: "team-tag-style-indigo",
  blue: "team-tag-style-blue",
  cyan: "team-tag-style-cyan",
  teal: "team-tag-style-teal",
  green: "team-tag-style-green",
  amber: "team-tag-style-amber",
  orange: "team-tag-style-orange",
  rose: "team-tag-style-rose",
  violet: "team-tag-style-violet",
  slate: "team-tag-style-slate"
};

export function teamTagClassName(style?: TeamTagStyle | null, unassigned = false) {
  return ["team-chip", unassigned ? "unassigned" : style ? tagStyleClasses[style] : ""]
    .filter(Boolean)
    .join(" ");
}
