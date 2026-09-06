import { createSlice, type PayloadAction } from '@reduxjs/toolkit'

export type ThemePreference = 'light' | 'dark' | 'system'

interface UiState {
  theme: ThemePreference
  agentPanelOpen: boolean
  agentPanelWidth: number
  agentPanelPage: 'chat' | 'history'
  // A one-way "hide" for the Overview setup checklist. Deliberately not
  // "completed": completion is always re-derived from live pulse data and
  // wins regardless of this flag — persisting it would freeze a moment in
  // time, and un-completing (a deleted mailbox) must resurface the panel.
  setupChecklistDismissed: boolean
}

// Stored as a tri-state rather than a resolved boolean: 'system' must keep
// tracking the OS preference as it changes, while an explicit choice must
// survive both a reload and a later OS-level flip. Collapsing this to
// `dark: boolean` loses the difference between "wants light" and "OS is light".
const initialState: UiState = {
  theme: 'system',
  agentPanelOpen: false,
  agentPanelWidth: 420,
  agentPanelPage: 'chat',
  setupChecklistDismissed: false,
}

const uiSlice = createSlice({
  name: 'ui',
  initialState,
  reducers: {
    setTheme: (s, action: PayloadAction<ThemePreference>) => {
      s.theme = action.payload
    },
    setAgentPanelOpen: (s, action: PayloadAction<boolean>) => {
      s.agentPanelOpen = action.payload
    },
    toggleAgentPanel: (s) => {
      s.agentPanelOpen = !s.agentPanelOpen
    },
    setAgentPanelWidth: (s, action: PayloadAction<number>) => {
      s.agentPanelWidth = Math.max(340, Math.min(640, action.payload))
    },
    setAgentPanelPage: (s, action: PayloadAction<'chat' | 'history'>) => {
      s.agentPanelPage = action.payload
    },
    // One-way by design: nothing un-dismisses. The checklist also unmounts
    // itself the moment every step derives complete, dismissed or not.
    dismissSetupChecklist: (s) => {
      s.setupChecklistDismissed = true
    },
  },
})

export const {
  setTheme,
  setAgentPanelOpen,
  toggleAgentPanel,
  setAgentPanelWidth,
  setAgentPanelPage,
  dismissSetupChecklist,
} = uiSlice.actions
export default uiSlice.reducer
