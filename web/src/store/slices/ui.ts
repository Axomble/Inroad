import { createSlice, type PayloadAction } from '@reduxjs/toolkit'

export type ThemePreference = 'light' | 'dark' | 'system'

interface UiState {
  theme: ThemePreference
  agentPanelOpen: boolean
  agentPanelWidth: number
  agentPanelPage: 'chat' | 'history'
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
  },
})

export const {
  setTheme,
  setAgentPanelOpen,
  toggleAgentPanel,
  setAgentPanelWidth,
  setAgentPanelPage,
} = uiSlice.actions
export default uiSlice.reducer
