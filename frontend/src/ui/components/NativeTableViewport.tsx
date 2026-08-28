import {
  forwardRef,
  useCallback,
  useEffect,
  useImperativeHandle,
  useLayoutEffect,
  useRef,
  useState,
  type HTMLAttributes,
  type ReactNode
} from "react";

type ScrollState = { overflow: boolean; top: boolean; bottom: boolean };

export type NativeTableViewportProps = Omit<HTMLAttributes<HTMLDivElement>, "children"> & {
  children: ReactNode;
};

const initialScrollState: ScrollState = { overflow: false, top: false, bottom: false };

/** Keep native-table scroll affordances aligned with AdminTable. */
export const NativeTableViewport = forwardRef<HTMLDivElement, NativeTableViewportProps>(function NativeTableViewport({
  children,
  className = "",
  onScroll,
  tabIndex,
  ...props
}, forwardedRef) {
  const viewportRef = useRef<HTMLDivElement>(null);
  const frameRef = useRef<number | null>(null);
  const [scrollState, setScrollState] = useState<ScrollState>(initialScrollState);
  useImperativeHandle(forwardedRef, () => viewportRef.current as HTMLDivElement, []);

  const update = useCallback(() => {
    const viewport = viewportRef.current;
    if (!viewport) return;
    const maximum = Math.max(0, viewport.scrollHeight - viewport.clientHeight);
    const horizontalMaximum = Math.max(0, viewport.scrollWidth - viewport.clientWidth);
    const next = {
      overflow: maximum > 1 || horizontalMaximum > 1,
      top: viewport.scrollTop > 1,
      bottom: viewport.scrollTop < maximum - 1
    };
    setScrollState((current) => current.overflow === next.overflow && current.top === next.top && current.bottom === next.bottom
      ? current
      : next);
  }, []);

  const scheduleUpdate = useCallback(() => {
    if (frameRef.current !== null) window.cancelAnimationFrame(frameRef.current);
    frameRef.current = window.requestAnimationFrame(() => {
      frameRef.current = null;
      update();
    });
  }, [update]);

  useLayoutEffect(scheduleUpdate);
  useEffect(() => {
    const viewport = viewportRef.current;
    if (!viewport) return;
    const resize = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(scheduleUpdate);
    const mutation = typeof MutationObserver === "undefined" ? null : new MutationObserver(scheduleUpdate);
    resize?.observe(viewport);
    if (viewport.firstElementChild instanceof HTMLElement) resize?.observe(viewport.firstElementChild);
    mutation?.observe(viewport, { childList: true, subtree: true, characterData: true });
    window.addEventListener("resize", scheduleUpdate);
    scheduleUpdate();
    return () => {
      resize?.disconnect();
      mutation?.disconnect();
      window.removeEventListener("resize", scheduleUpdate);
      if (frameRef.current !== null) window.cancelAnimationFrame(frameRef.current);
    };
  }, [scheduleUpdate]);

  return (
    <div
      {...props}
      ref={viewportRef}
      className={[
        "native-table-viewport",
        scrollState.top ? "can-scroll-up" : "",
        scrollState.bottom ? "can-scroll-down" : "",
        className
      ].filter(Boolean).join(" ")}
      data-scroll-overflow={scrollState.overflow ? "true" : "false"}
      onScroll={(event) => {
        update();
        onScroll?.(event);
      }}
      tabIndex={tabIndex ?? (scrollState.overflow ? 0 : undefined)}
    >
      {children}
    </div>
  );
});
