import { useState } from 'react'
import { fireEvent, render, screen } from '@testing-library/react'
import { expect, test, vi } from 'vitest'
import { Combobox, type ComboboxListState, type ComboboxOption, type ComboboxPaging } from '../combobox'

const acme: ComboboxOption = { id: 'co-1', label: 'Acme', detail: 'acme.test' }
const globex: ComboboxOption = { id: 'co-2', label: 'Globex' }
const initech: ComboboxOption = { id: 'co-3', label: 'Initech' }

const labels = {
  placeholder: 'Search companies',
  list: 'Companies',
  empty: 'Nothing matches.',
  none: 'No company',
  clear: 'Clear the selected company',
}

// Module-level so the Harness defaults are stable references across renders.
const defaultOptions = [acme, globex]
const idleList: ComboboxListState = { loading: false }
const ignore = () => {}

function Harness({
  options = defaultOptions,
  initial = null,
  list = idleList,
  paging,
  onQueryChange = ignore,
  onSelect = ignore,
  hint,
}: {
  options?: ComboboxOption[]
  initial?: ComboboxOption | null
  list?: ComboboxListState
  paging?: ComboboxPaging
  onQueryChange?: (next: string) => void
  onSelect?: (option: ComboboxOption | null) => void
  hint?: string
}) {
  const [query, setQuery] = useState('')
  const [selected, setSelected] = useState(initial)
  return (
    <>
      <label htmlFor="company">Company</label>
      <Combobox
        inputId="company"
        query={query}
        onQueryChange={(next) => {
          setQuery(next)
          onQueryChange(next)
        }}
        options={options}
        selected={selected}
        onSelect={(option) => {
          setSelected(option)
          onSelect(option)
        }}
        list={list}
        paging={paging}
        labels={{ ...labels, hint }}
      />
    </>
  )
}

test('typing reports the query and the field is a labelled combobox over a listbox', () => {
  const onQueryChange = vi.fn()
  render(<Harness onQueryChange={onQueryChange} />)

  const input = screen.getByRole('combobox', { name: 'Company' })
  fireEvent.change(input, { target: { value: 'glo' } })

  expect(onQueryChange).toHaveBeenLastCalledWith('glo')
  expect(input).toHaveValue('glo')
  expect(input).toHaveAttribute('aria-controls', screen.getByRole('listbox', { name: 'Companies' }).id)
})

test('clicking an option selects it and marks it selected', () => {
  const onSelect = vi.fn()
  render(<Harness onSelect={onSelect} />)

  fireEvent.click(screen.getByRole('option', { name: 'Globex' }))

  expect(onSelect).toHaveBeenCalledWith(globex)
  expect(screen.getByRole('option', { name: 'Globex' })).toHaveAttribute('aria-selected', 'true')
  expect(screen.getByRole('option', { name: /Acme/ })).toHaveAttribute('aria-selected', 'false')
})

test('arrow keys move the highlight and Enter picks it', () => {
  const onSelect = vi.fn()
  render(<Harness options={[acme, globex, initech]} onSelect={onSelect} />)
  const input = screen.getByRole('combobox')

  fireEvent.keyDown(input, { key: 'ArrowDown' })
  fireEvent.keyDown(input, { key: 'ArrowDown' })
  fireEvent.keyDown(input, { key: 'ArrowDown' })
  fireEvent.keyDown(input, { key: 'ArrowDown' }) // clamps at the last row
  fireEvent.keyDown(input, { key: 'ArrowUp' })
  expect(input).toHaveAttribute('aria-activedescendant', screen.getByRole('option', { name: 'Globex' }).id)

  fireEvent.keyDown(input, { key: 'Enter' })
  expect(onSelect).toHaveBeenCalledWith(globex)
})

test('Enter with nothing highlighted selects nothing', () => {
  const onSelect = vi.fn()
  render(<Harness onSelect={onSelect} />)

  fireEvent.keyDown(screen.getByRole('combobox'), { key: 'Enter' })

  expect(onSelect).not.toHaveBeenCalled()
})

test('the current selection stays visible even when it is not among the loaded options', () => {
  const linked: ComboboxOption = { id: 'co-99', label: 'Past The First Page' }
  render(<Harness options={[acme]} initial={linked} />)

  expect(screen.getByText('Past The First Page')).toBeInTheDocument()
  expect(screen.queryByRole('option', { name: 'Past The First Page' })).not.toBeInTheDocument()
})

test('clearing the selection reports null and says what empty means', () => {
  const onSelect = vi.fn()
  render(<Harness initial={acme} onSelect={onSelect} />)

  fireEvent.click(screen.getByRole('button', { name: 'Clear the selected company' }))

  expect(onSelect).toHaveBeenCalledWith(null)
  expect(screen.getByText('No company')).toBeInTheDocument()
})

test('an empty result says so, but not while loading or failed', () => {
  const { rerender } = render(<Harness options={[]} />)
  expect(screen.getByText('Nothing matches.')).toBeInTheDocument()

  rerender(<Harness options={[]} list={{ loading: true }} />)
  expect(screen.queryByText('Nothing matches.')).not.toBeInTheDocument()
  expect(screen.getByRole('status')).toHaveTextContent('Loading')

  rerender(<Harness options={[]} list={{ loading: false, error: 'The list failed.' }} />)
  expect(screen.queryByText('Nothing matches.')).not.toBeInTheDocument()
})

test('a failed list shows the caller copy and a retry', () => {
  const onRetry = vi.fn()
  render(<Harness options={[]} list={{ loading: false, error: 'The list failed.', onRetry }} />)

  expect(screen.getByRole('alert')).toHaveTextContent('The list failed.')
  fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
  expect(onRetry).toHaveBeenCalledTimes(1)
})

test('load more is offered only while more exists, and is disabled while loading', () => {
  const onLoadMore = vi.fn()
  const { rerender } = render(<Harness paging={{ hasMore: true, loading: false, onLoadMore }} />)

  fireEvent.click(screen.getByRole('button', { name: 'Load more' }))
  expect(onLoadMore).toHaveBeenCalledTimes(1)

  rerender(<Harness paging={{ hasMore: true, loading: true, onLoadMore }} />)
  expect(screen.getByRole('button', { name: 'Load more' })).toBeDisabled()

  rerender(<Harness paging={{ hasMore: false, loading: false, onLoadMore }} />)
  expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument()
})

test('scrolling to the bottom of the list loads the next page, but never twice at once', () => {
  const onLoadMore = vi.fn()
  const { rerender } = render(<Harness paging={{ hasMore: true, loading: false, onLoadMore }} />)
  const listbox = screen.getByRole('listbox')
  Object.defineProperty(listbox, 'scrollHeight', { configurable: true, value: 500 })
  Object.defineProperty(listbox, 'clientHeight', { configurable: true, value: 200 })

  Object.defineProperty(listbox, 'scrollTop', { configurable: true, value: 50 })
  fireEvent.scroll(listbox)
  expect(onLoadMore).not.toHaveBeenCalled()

  Object.defineProperty(listbox, 'scrollTop', { configurable: true, value: 290 })
  fireEvent.scroll(listbox)
  expect(onLoadMore).toHaveBeenCalledTimes(1)

  rerender(<Harness paging={{ hasMore: true, loading: true, onLoadMore }} />)
  fireEvent.scroll(listbox)
  expect(onLoadMore).toHaveBeenCalledTimes(1)
})

test('a failed page is reported and stops scroll from retrying it in a loop', () => {
  const onLoadMore = vi.fn()
  render(<Harness paging={{ hasMore: true, loading: false, error: 'More failed.', onLoadMore }} />)
  const listbox = screen.getByRole('listbox')
  Object.defineProperty(listbox, 'scrollHeight', { configurable: true, value: 100 })
  Object.defineProperty(listbox, 'clientHeight', { configurable: true, value: 100 })

  fireEvent.scroll(listbox)

  expect(onLoadMore).not.toHaveBeenCalled()
  expect(screen.getByRole('alert')).toHaveTextContent('More failed.')
  // The explicit button is still the way to try again.
  fireEvent.click(screen.getByRole('button', { name: 'Load more' }))
  expect(onLoadMore).toHaveBeenCalledTimes(1)
})

test('a hint is announced as the field description', () => {
  render(<Harness hint="Type at least 2 characters to search." />)

  expect(screen.getByRole('combobox')).toHaveAccessibleDescription('Type at least 2 characters to search.')
})
