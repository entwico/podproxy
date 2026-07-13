// shared echo service definitions, used by the vitest host and the spawned client runners

const { create, toBinary } = require('@bufbuild/protobuf');
const { FileDescriptorProtoSchema } = require('@bufbuild/protobuf/wkt');
const { fileDesc, serviceDesc } = require('@bufbuild/protobuf/codegenv2');

// runtime-built descriptor equivalent to:
//   syntax = "proto3";
//   package podproxy.it;
//   message EchoRequest { string message = 1; }
//   message EchoResponse { string message = 1; }
//   service EchoService { rpc Echo(EchoRequest) returns (EchoResponse); }
const FIELD_TYPE_STRING = 9;
const FIELD_LABEL_OPTIONAL = 1;

const fileDescriptorProto = create(FileDescriptorProtoSchema, {
  name: 'podproxy/it/echo.proto',
  package: 'podproxy.it',
  syntax: 'proto3',
  messageType: [
    {
      name: 'EchoRequest',
      field: [{ name: 'message', number: 1, type: FIELD_TYPE_STRING, label: FIELD_LABEL_OPTIONAL, jsonName: 'message' }],
    },
    {
      name: 'EchoResponse',
      field: [{ name: 'message', number: 1, type: FIELD_TYPE_STRING, label: FIELD_LABEL_OPTIONAL, jsonName: 'message' }],
    },
  ],
  service: [
    {
      name: 'EchoService',
      method: [
        {
          name: 'Echo',
          inputType: '.podproxy.it.EchoRequest',
          outputType: '.podproxy.it.EchoResponse',
        },
      ],
    },
  ],
});

const file = fileDesc(Buffer.from(toBinary(FileDescriptorProtoSchema, fileDescriptorProto)).toString('base64'));

exports.EchoService = serviceDesc(file, 0);

// plain JSON-over-grpc service definition for @grpc/grpc-js (no codegen needed)
exports.jsonEchoDefinition = {
  echo: {
    path: '/podproxy.it.JsonEcho/Echo',
    requestStream: false,
    responseStream: false,
    requestSerialize: (value) => Buffer.from(JSON.stringify(value)),
    requestDeserialize: (buffer) => JSON.parse(buffer.toString()),
    responseSerialize: (value) => Buffer.from(JSON.stringify(value)),
    responseDeserialize: (buffer) => JSON.parse(buffer.toString()),
  },
};

// response padding forces multi-frame responses so buffered readable data is exercised
exports.RESPONSE_PADDING = 'x'.repeat(65536);
