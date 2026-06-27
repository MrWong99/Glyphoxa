// MockEventSource is a minimal jsdom-less EventSource stand-in for vitest:
// jsdom ships none, and the Session screen opens one for the live transcript
// (#73). Registered as the global EventSource in vitest.setup.ts; a test grabs
// the constructed instance via MockEventSource.last() and drives frames with
// emit().

type Listener = (e: MessageEvent) => void;

export class MockEventSource {
  static instances: MockEventSource[] = [];

  /** Clear the registry between tests (called from a global beforeEach). */
  static reset() {
    MockEventSource.instances = [];
  }

  /** The most recently constructed instance (the screen's open stream). */
  static last(): MockEventSource | undefined {
    return MockEventSource.instances[MockEventSource.instances.length - 1];
  }

  url: string;
  closed = false;
  private listeners: Record<string, Listener[]> = {};

  constructor(url: string) {
    this.url = url;
    MockEventSource.instances.push(this);
  }

  addEventListener(type: string, cb: Listener) {
    (this.listeners[type] ||= []).push(cb);
  }

  removeEventListener(type: string, cb: Listener) {
    this.listeners[type] = (this.listeners[type] || []).filter((l) => l !== cb);
  }

  close() {
    this.closed = true;
  }

  /** Dispatch a named SSE event carrying JSON-encoded data, like the relay. */
  emit(type: string, data: unknown) {
    const e = { data: JSON.stringify(data) } as MessageEvent;
    (this.listeners[type] || []).forEach((cb) => cb(e));
  }
}
