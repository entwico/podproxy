// SOCKS5 wire format (RFC 1928), the subset podproxy speaks: no auth, CONNECT only

export const SOCKS_VERSION = 0x05;

export enum SocksMethod {
  NoAuth = 0x00,
}

export enum SocksCommand {
  Connect = 0x01,
}

export enum SocksAddressType {
  IPv4 = 0x01,
  Domain = 0x03,
  IPv6 = 0x04,
}

export enum SocksReply {
  Succeeded = 0x00,
  GeneralFailure = 0x01,
  ConnectionNotAllowed = 0x02,
  NetworkUnreachable = 0x03,
  HostUnreachable = 0x04,
  ConnectionRefused = 0x05,
  TtlExpired = 0x06,
  CommandNotSupported = 0x07,
  AddressTypeNotSupported = 0x08,
}

const REPLY_MESSAGES: Record<SocksReply, string> = {
  [SocksReply.Succeeded]: 'succeeded',
  [SocksReply.GeneralFailure]: 'general SOCKS server failure',
  [SocksReply.ConnectionNotAllowed]: 'connection not allowed by ruleset',
  [SocksReply.NetworkUnreachable]: 'network unreachable',
  [SocksReply.HostUnreachable]: 'host unreachable',
  [SocksReply.ConnectionRefused]: 'connection refused',
  [SocksReply.TtlExpired]: 'TTL expired',
  [SocksReply.CommandNotSupported]: 'command not supported',
  [SocksReply.AddressTypeNotSupported]: 'address type not supported',
};

export function replyMessage(code: number): string {
  return REPLY_MESSAGES[code as SocksReply] ?? `unknown reply code ${code}`;
}

// VER NMETHODS METHODS
export function encodeGreeting(): Buffer {
  return Buffer.from([SOCKS_VERSION, 1, SocksMethod.NoAuth]);
}

// VER METHOD
export function encodeMethodReply(method: SocksMethod): Buffer {
  return Buffer.from([SOCKS_VERSION, method]);
}

// VER CMD RSV ATYP=domain LEN HOST PORT — always domain form, podproxy resolves hostnames
export function encodeConnectRequest(host: string, port: number): Buffer {
  const hostBuf = Buffer.from(host);
  const request = Buffer.alloc(7 + hostBuf.length);

  request[0] = SOCKS_VERSION;
  request[1] = SocksCommand.Connect;
  request[2] = 0x00;
  request[3] = SocksAddressType.Domain;
  request[4] = hostBuf.length;
  hostBuf.copy(request, 5);
  request.writeUInt16BE(port, 5 + hostBuf.length);

  return request;
}

// VER REP RSV ATYP=ipv4 BND.ADDR BND.PORT, with a zeroed bind address
export function encodeReply(code: SocksReply): Buffer {
  return Buffer.from([SOCKS_VERSION, code, 0x00, SocksAddressType.IPv4, 0, 0, 0, 0, 0, 0]);
}

export function decodeConnectRequest(request: Buffer): { host: string; port: number } | null {
  if (request[0] !== SOCKS_VERSION || request[1] !== SocksCommand.Connect) {
    return null;
  }

  const atyp = request[3];

  if (atyp === SocksAddressType.Domain) {
    const length = request[4];

    return {
      host: request.subarray(5, 5 + length).toString(),
      port: request.readUInt16BE(5 + length),
    };
  }

  if (atyp === SocksAddressType.IPv4) {
    return {
      host: Array.from(request.subarray(4, 8)).join('.'),
      port: request.readUInt16BE(8),
    };
  }

  return null;
}
