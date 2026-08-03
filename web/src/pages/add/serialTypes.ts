/**
 * Shared Web Serial shapes for the serial-add wizard.
 * Chromium only; typings are local so we do not depend on DOM lib serial drafts.
 */

export type SerialParity = "none" | "even" | "odd";
export type SerialFlowControl = "none" | "hardware";

export type SerialParams = {
  baudRate: number;
  dataBits: 7 | 8;
  stopBits: 1 | 2;
  parity: SerialParity;
  flowControl: SerialFlowControl;
};

export type SerialPortLike = {
  open(options: {
    baudRate: number;
    dataBits?: number;
    stopBits?: number;
    parity?: SerialParity;
    flowControl?: SerialFlowControl;
    bufferSize?: number;
  }): Promise<void>;
  close(): Promise<void>;
  readable: ReadableStream<Uint8Array> | null;
  writable: WritableStream<Uint8Array> | null;
};

export type SerialLike = {
  requestPort(): Promise<SerialPortLike>;
};

export function serialApi(): SerialLike | undefined {
  return (navigator as Navigator & { serial?: SerialLike }).serial;
}

export function formatSerialParams(p: SerialParams): string {
  var parityLetter = p.parity === "none" ? "N" : p.parity === "even" ? "E" : "O";
  var flow = p.flowControl === "hardware" ? " hwfc" : "";
  return `${p.baudRate} ${p.dataBits}${parityLetter}${p.stopBits}${flow}`;
}

export const DEFAULT_SERIAL_PARAMS: SerialParams = {
  baudRate: 9600,
  dataBits: 8,
  stopBits: 1,
  parity: "none",
  flowControl: "none",
};
