import { createSlice, type PayloadAction } from '@reduxjs/toolkit'

export type ThemePreference = 'light' | 'dark' | 'system'

interface UiState {
  theme: ThemePreference
}

// Stored as a tri-state rather than a resolved boolean: 'system' must keep
// tracking the OS preference as it changes, while an explicit choice must
// survive both a reload and a later OS-level flip. Collapsing this to
// `dark: boolean` loses the difference between "wants light" and "OS is light".
const initialState: UiState = { theme: 'system' }

const uiSlice = createSlice({
  name: 'ui',
  initialState,
  reducers: {
    setTheme: (s, action: PayloadAction<ThemePreference>) => {
      s.theme = action.payload
    },
  },
})

export const { setTheme } = uiSlice.actions
export default uiSlice.reducer
