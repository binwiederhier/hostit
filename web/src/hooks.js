import { useEffect } from "react";

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
