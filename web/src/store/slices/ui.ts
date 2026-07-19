import { createSlice } from '@reduxjs/toolkit'

interface UiState { sidebarOpen: boolean }
const initialState: UiState = { sidebarOpen: true }

const uiSlice = createSlice({
  name: 'ui',
  initialState,
  reducers: {
    toggleSidebar: (s) => { s.sidebarOpen = !s.sidebarOpen },
  },
})

export const { toggleSidebar } = uiSlice.actions
export default uiSlice.reducer
