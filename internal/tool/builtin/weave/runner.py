"""Execute one script with asynchronous ElBot tool proxies."""
import ast
import asyncio
import itertools
import json
import os
import sys


async def main():
    loop = asyncio.get_running_loop()
    reader = asyncio.StreamReader(limit=sys.maxsize)
    protocol = asyncio.StreamReaderProtocol(reader)
    if os.name == "nt":
        transport, _ = await loop.create_pipe_connection(lambda: protocol, sys.argv[1])
    else:
        await loop.connect_read_pipe(lambda: protocol, os.fdopen(3, "rb"))
        transport, _ = await loop.connect_write_pipe(
            asyncio.streams.FlowControlMixin, os.fdopen(4, "wb")
        )

    # Signal that uv finished preparing the environment before Go dispatches code.
    transport.write(b"\x01")
    init = json.loads(await reader.readline())
    pending = {}
    ids = itertools.count(1)

    def proxy(name):
        async def call(**kwargs):
            seq = next(ids)
            message = json.dumps({"id": seq, "name": name, "args": kwargs})
            pending[seq] = future = loop.create_future()
            transport.write(message.encode() + b"\n")
            return await future
        return call

    async def receive():
        try:
            async for line in reader:
                reply = json.loads(line)
                future = pending.pop(reply["id"])
                if future.done():
                    continue
                if reply["ok"]:
                    future.set_result(reply["text"])
                else:
                    future.set_exception(RuntimeError(reply["text"]))
        finally:
            for future in pending.values():
                if not future.done():
                    future.set_exception(RuntimeError("ElBot tool pipe closed"))

    receiver = asyncio.create_task(receive())
    try:
        scope = {name: proxy(name) for name in init["names"]}
        scope["__name__"] = "__main__"
        scope["asyncio"] = asyncio
        code = compile(init["code"], "<tool_weave>", "exec", ast.PyCF_ALLOW_TOP_LEVEL_AWAIT)
        result = eval(code, scope)
        if result is not None:
            await result
    finally:
        receiver.cancel()
        await asyncio.gather(receiver, return_exceptions=True)
        transport.close()


asyncio.run(main())
