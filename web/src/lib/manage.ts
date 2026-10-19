import { createContext, useContext } from "react";

/**
 * Whether the person may change what the page shows. Governance and Settings
 * pages are read-only for auditors: the page hides its forms and buttons,
 * and the API refuses the changes anyway.
 */
export const ManageContext = createContext(true);

export const useCanManage = () => useContext(ManageContext);
