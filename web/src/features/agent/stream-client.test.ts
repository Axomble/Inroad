import { openAgentStream, readSSE } from './stream-client'

function streamingResponse(chunks: string[]): Response {
  const encoder = new TextEncoder()
  return new Response(new ReadableStream({
    start(controller) {
      for (const chunk of chunks) controller.enqueue(encoder.encode(chunk))
      controller.close()
    },
  }))
}

describe('agent SSE client', () => {
  it('parses frames when CRLF boundaries are split across chunks', async () => {
    const frames: Array<{ id: number; event: { type: string; text?: string } }> = []
    const response = streamingResponse([
      'id: 4\r\ndata: {"type":"text_delta","text":"Hi"}\r',
      '\n\r\nid: 5\ndata: {"type":"done"}\n\n',
    ])

    await readSSE(response, (frame) => frames.push(frame), new AbortController().signal)

    expect(frames).toEqual([
      { id: 4, event: { type: 'text_delta', text: 'Hi' } },
      { id: 5, event: { type: 'done' } },
    ])
  })

  it('sends resumable authenticated stream requests', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response('', { status: 200 }))

    await openAgentStream('thread-1', 'token-1', 17, new AbortController().signal)

    const call = fetchMock.mock.calls[0]
    expect(call).toBeDefined()
    if (!call) return
    expect(String(call[0])).toContain('/agent/threads/thread-1/stream?after_seq=17')
    expect(call[1]).toMatchObject({
      credentials: 'include',
      headers: {
        Accept: 'text/event-stream',
        Authorization: 'Bearer token-1',
        'Last-Event-ID': '17',
      },
    })
    fetchMock.mockRestore()
  })
})
