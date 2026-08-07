import { createContext, useContext } from "react";

// A tiny channel for the app detail page to hand its identity (name + live status)
// up to the nav, so on small screens the nav can show it in place of the logo and
// there is a single top bar. The value is a setter; App owns the state.
export const AppHeaderContext = createContext(() => {});
export const useSetAppHeader = () => useContext(AppHeaderContext);
