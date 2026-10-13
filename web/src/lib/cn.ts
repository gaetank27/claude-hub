// cn joins class names, dropping falsy values. A tiny clsx-style helper so we
// don't pull in a dependency just to compose conditional Tailwind classes.
export type ClassValue = string | number | false | null | undefined;

export function cn(...parts: ClassValue[]): string {
  return parts.filter(Boolean).join(" ");
}
