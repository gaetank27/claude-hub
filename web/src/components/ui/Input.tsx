import { forwardRef } from "react";
import type { InputHTMLAttributes, SelectHTMLAttributes, TextareaHTMLAttributes } from "react";
import { cn } from "../../lib/cn";

// Shared field surface for text-like controls. outline-none is intentional: the
// global :focus-visible rule (theme.css) supplies the keyboard ring, while
// focus:border-accent-dim gives a softer pointer-focus cue.
const FIELD =
  "w-full bg-bg border border-border rounded-control px-3 py-2 text-sm " +
  "outline-none transition-colors focus:border-accent-dim placeholder:text-muted/60";

type InputProps = InputHTMLAttributes<HTMLInputElement> & { invalid?: boolean };
export const Input = forwardRef<HTMLInputElement, InputProps>(function Input(
  { className, invalid, ...rest },
  ref,
) {
  return <input ref={ref} className={cn(FIELD, invalid && "border-danger", className)} {...rest} />;
});

type TextareaProps = TextareaHTMLAttributes<HTMLTextAreaElement> & { invalid?: boolean };
export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(function Textarea(
  { className, invalid, ...rest },
  ref,
) {
  return (
    <textarea ref={ref} className={cn(FIELD, "resize-y", invalid && "border-danger", className)} {...rest} />
  );
});

type SelectProps = SelectHTMLAttributes<HTMLSelectElement> & { invalid?: boolean };
export const Select = forwardRef<HTMLSelectElement, SelectProps>(function Select(
  { className, invalid, children, ...rest },
  ref,
) {
  return (
    <select ref={ref} className={cn(FIELD, invalid && "border-danger", className)} {...rest}>
      {children}
    </select>
  );
});
