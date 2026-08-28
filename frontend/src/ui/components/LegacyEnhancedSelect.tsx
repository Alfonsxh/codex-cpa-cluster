import {
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type FocusEvent,
  type KeyboardEvent
} from "react";

const OPEN_EVENT = "cpa:legacy-enhanced-select-open";

export type LegacyEnhancedSelectOption<Value extends string> = {
  value: Value;
  label: string;
};

export type LegacyEnhancedSelectProps<Value extends string> = {
  id?: string;
  label: string;
  value: Value;
  options: Array<LegacyEnhancedSelectOption<Value>>;
  onChange: (value: Value) => void;
  disabled?: boolean;
  title?: string;
};

/** React implementation of the frozen v1 enhanceSelect() DOM and state machine. */
export function LegacyEnhancedSelect<Value extends string>({
  id = "",
  label,
  value,
  options,
  onChange,
  disabled = false,
  title
}: LegacyEnhancedSelectProps<Value>) {
  const [open, setOpen] = useState(false);
  const instanceId = useId();
  const wrapperRef = useRef<HTMLSpanElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuId = `${id}-menu`;
  const selected = options.find((option) => option.value === value) ?? options[0];
  const selectedLabel = selected?.label || "请选择";

  useEffect(() => {
    const closeOtherSelect = (event: Event) => {
      if (!(event instanceof CustomEvent) || event.detail === instanceId) return;
      setOpen(false);
    };
    const closeFromOutside = (event: PointerEvent) => {
      if (wrapperRef.current?.contains(event.target as Node)) return;
      setOpen(false);
    };
    document.addEventListener(OPEN_EVENT, closeOtherSelect);
    document.addEventListener("pointerdown", closeFromOutside);
    return () => {
      document.removeEventListener(OPEN_EVENT, closeOtherSelect);
      document.removeEventListener("pointerdown", closeFromOutside);
    };
  }, [instanceId]);

  useLayoutEffect(() => {
    if (!open) return;
    wrapperRef.current?.querySelector<HTMLButtonElement>('[role="option"][aria-selected="true"]')?.focus();
  }, [open]);

  const openMenu = () => {
    if (disabled) return;
    document.dispatchEvent(new CustomEvent(OPEN_EVENT, { detail: instanceId }));
    setOpen(true);
  };

  const closeAndFocusTrigger = () => {
    setOpen(false);
    triggerRef.current?.focus();
  };

  const selectOption = (nextValue: Value) => {
    const previous = value;
    closeAndFocusTrigger();
    if (nextValue !== previous || nextValue === "custom") onChange(nextValue);
  };

  const handleKeyDown = (event: KeyboardEvent<HTMLSpanElement>) => {
    if (event.key === "Escape") {
      closeAndFocusTrigger();
      return;
    }
    if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    if (!open) {
      openMenu();
      return;
    }
    const optionButtons = Array.from(
      wrapperRef.current?.querySelectorAll<HTMLButtonElement>("[data-select-value]") ?? []
    );
    const current = Math.max(0, optionButtons.indexOf(document.activeElement as HTMLButtonElement));
    const next = event.key === "Home"
      ? 0
      : event.key === "End"
        ? optionButtons.length - 1
        : Math.min(optionButtons.length - 1, Math.max(0, current + (event.key === "ArrowDown" ? 1 : -1)));
    optionButtons[next]?.focus();
  };

  const handleFocusOut = (event: FocusEvent<HTMLSpanElement>) => {
    if (wrapperRef.current?.contains(event.relatedTarget as Node | null)) return;
    setOpen(false);
  };

  return (
    <>
      <select
        id={id || undefined}
        className="enhanced-select-native"
        value={value}
        tabIndex={-1}
        aria-hidden="true"
        disabled={disabled}
        onChange={(event) => onChange(event.currentTarget.value as Value)}
      >
        {options.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
      </select>
      <span
        ref={wrapperRef}
        className="enhanced-select"
        onKeyDown={handleKeyDown}
        onBlur={handleFocusOut}
      >
        <button
          ref={triggerRef}
          className="enhanced-select-trigger"
          type="button"
          aria-label={`${label}：${selectedLabel}`}
          aria-haspopup="listbox"
          aria-expanded={open}
          aria-controls={menuId}
          title={title || selectedLabel}
          disabled={disabled}
          onClick={() => open ? setOpen(false) : openMenu()}
        >
          <span data-enhanced-select-value>{selectedLabel}</span>
          <span className="enhanced-select-caret" aria-hidden="true">⌄</span>
        </button>
        <span
          className="enhanced-select-menu"
          id={menuId}
          role="listbox"
          aria-label={label}
          hidden={!open}
        >
          {options.map((option) => (
            <button
              key={option.value}
              type="button"
              role="option"
              data-select-value={option.value}
              aria-selected={option.value === value}
              onClick={() => selectOption(option.value)}
            >
              <span className="enhanced-select-check" aria-hidden="true">{option.value === value ? "✓" : ""}</span>
              <span>{option.label}</span>
            </button>
          ))}
        </span>
      </span>
    </>
  );
}
