import { httpStatus, isFetchBaseQueryError } from './rtk-error'

test('isFetchBaseQueryError narrows objects carrying a status', () => {
  expect(isFetchBaseQueryError({ status: 404, data: undefined })).toBe(true)
  expect(isFetchBaseQueryError({ status: 'FETCH_ERROR', error: 'x' })).toBe(true)
  expect(isFetchBaseQueryError({ message: 'plain error' })).toBe(false)
  expect(isFetchBaseQueryError(null)).toBe(false)
  expect(isFetchBaseQueryError(undefined)).toBe(false)
})

test('httpStatus returns the numeric HTTP status only', () => {
  expect(httpStatus({ status: 501, data: undefined })).toBe(501)
  expect(httpStatus({ status: 'TIMEOUT_ERROR', error: 'x' })).toBeUndefined()
  expect(httpStatus({ name: 'Error', message: 'boom' })).toBeUndefined()
  expect(httpStatus(undefined)).toBeUndefined()
})
