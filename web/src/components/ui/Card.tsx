import type { ReactNode } from "react";
import { Link } from "react-router-dom";
import { cn } from "../../lib/cn";

type CardPadding = "sm" | "md" | "none";

const PAD: Record<CardPadding, string> = {
  none: "",
  sm: "p-3",
  md: "p-4",
};

type CommonProps = {
  interactive?: boolean;
  padding?: CardPadding;
  className?: string;
  children: ReactNode;
};

// Base surface used for tiles, list rows and panels. `interactive` adds the
// hover/press affordance for clickable cards. Renders as a div, a router Link or
// a button depending on the props passed, so every clickable tile shares one
// look (radius-card, border-as-elevation).
function base(interactive: boolean, padding: CardPadding, className?: string): string {
  return cn(
    "block bg-panel border border-border rounded-card text-left",
    PAD[padding],
    interactive &&
      "transition-colors duration-150 hover:border-accent-dim motion-safe:active:scale-[.99] cursor-pointer",
    className,
  );
}

type CardProps =
  | ({ as?: "div" } & CommonProps)
  | ({ as: "link"; to: string } & CommonProps)
  | ({ as: "button"; onClick?: () => void; type?: "button" | "submit" } & CommonProps);

export function Card(props: CardProps) {
  const { interactive = false, padding = "md", className, children } = props;
  const cls = base(interactive, padding, className);

  if (props.as === "link") {
    return (
      <Link to={props.to} className={cls}>
        {children}
      </Link>
    );
  }
  if (props.as === "button") {
    return (
      <button type={props.type ?? "button"} onClick={props.onClick} className={cls}>
        {children}
      </button>
    );
  }
  return <div className={cls}>{children}</div>;
}
