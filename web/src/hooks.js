import { useEffect, useRef, useState } from "react";

// useReconnect runs onReconnect when connectivity likely returned: the browser
// fires "online", or the tab becomes visible again (e.g. after the laptop woke).
// Background polls stop showing errors and refetch here instead, so a transient
// drop heals on its own without a sticky banner.
export const useReconnect = (onReconnect) => {
  useEffect(() => {
    const heal = () => {
      if (document.visibilityState === "visible") {
        onReconnect();
      }
    };
    window.addEventListener("online", heal);
    document.addEventListener("visibilitychange", heal);
    return () => {
      window.removeEventListener("online", heal);
      document.removeEventListener("visibilitychange", heal);
    };
  }, [onReconnect]);
};

// useAppViewportHeight publishes the *visual* viewport height as the --app-vh CSS
// variable. On a phone the on-screen keyboard shrinks the visual viewport but not
// the layout viewport, so full-height views measured in dvh sit behind the
// keyboard. Driving their height from --app-vh instead keeps the composer right
// above the keyboard, like a chat app. Falls back to 100dvh when the var is unset.
export const useAppViewportHeight = () => {
  useEffect(() => {
    const vv = window.visualViewport;
    const apply = () => {
      const h = vv ? vv.height : window.innerHeight;
      document.documentElement.style.setProperty("--app-vh", `${Math.round(h)}px`);
    };
    apply();
    if (vv) {
      vv.addEventListener("resize", apply);
      vv.addEventListener("scroll", apply);
    }
    window.addEventListener("resize", apply);
    window.addEventListener("orientationchange", apply);
    return () => {
      if (vv) {
        vv.removeEventListener("resize", apply);
        vv.removeEventListener("scroll", apply);
      }
      window.removeEventListener("resize", apply);
      window.removeEventListener("orientationchange", apply);
    };
  }, []);
};

// A dropdown that closes on an outside click or Escape. Shared, because every
// menu in the app needs exactly this and three copies of it would drift.
export const useDropdown = () => {
  const [open, setOpen] = useState(false);
  const ref = useRef(null);
  useEffect(() => {
    if (!open) {
      return undefined;
    }
    const close = (e) => {
      if (ref.current && !ref.current.contains(e.target)) {
        setOpen(false);
      }
    };
    const onKey = (e) => e.key === "Escape" && setOpen(false);
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", close);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);
  return { open, setOpen, ref };
};
