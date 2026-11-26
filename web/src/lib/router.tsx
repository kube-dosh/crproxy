import { useEffect, useState, type AnchorHTMLAttributes, type MouseEvent } from "react";

const listeners = new Set<() => void>();

function emit() {
  for (const listener of listeners) listener();
}

export function navigate(path: string, options: { replace?: boolean } = {}) {
  if (path === window.location.pathname) return;
  if (options.replace) window.history.replaceState(null, "", path);
  else window.history.pushState(null, "", path);
  emit();
  window.scrollTo({ top: 0 });
}

export function usePathname() {
  const [pathname, setPathname] = useState(() => window.location.pathname || "/");
  useEffect(() => {
    const listener = () => setPathname(window.location.pathname || "/");
    listeners.add(listener);
    window.addEventListener("popstate", listener);
    return () => {
      listeners.delete(listener);
      window.removeEventListener("popstate", listener);
    };
  }, []);
  return pathname;
}

export function Link({ href, onClick, children, ...props }: AnchorHTMLAttributes<HTMLAnchorElement> & { href: string }) {
  const handleClick = (event: MouseEvent<HTMLAnchorElement>) => {
    onClick?.(event);
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    if (props.target && props.target !== "_self") return;
    if (/^[a-z]+:/i.test(href) || href.startsWith("//")) return;
    if (href.startsWith("#")) return;
    event.preventDefault();
    navigate(href);
  };
  return <a href={href} onClick={handleClick} {...props}>{children}</a>;
}
