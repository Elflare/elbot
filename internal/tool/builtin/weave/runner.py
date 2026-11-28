"""Execute one script with asynchronous ElBot tool proxies."""
import ast
import asyncio
import itertools
import json
import linecache
import os
import sys
import traceback


class ToolCallError(RuntimeError):
    def __init__(self, name, message):
        super().__init__(message)
        self.name = name


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
            reply = await future
            if not reply["ok"]:
                raise ToolCallError(name, reply["text"])
            return reply["text"]
        return call

    async def receive():
        try:
            async for line in reader:
                reply = json.loads(line)
                future = pending.pop(reply["id"])
                if future.done():
                    continue
                future.set_result(reply)
        finally:
            for future in pending.values():
                if not future.done():
                    future.set_exception(RuntimeError("ElBot tool pipe closed"))

    receiver = asyncio.create_task(receive())
    try:
        scope = {name: proxy(name) for name in init["names"]}
        scope["__name__"] = "__main__"
        scope["asyncio"] = asyncio
        # Give traceback the in-memory script's source lines, without writing a file.
        linecache.cache["<tool_weave>"] = (
            len(init["code"]), None, init["code"].splitlines(keepends=True), "<tool_weave>"
        )
        code = compile(init["code"], "<tool_weave>", "exec", ast.PyCF_ALLOW_TOP_LEVEL_AWAIT)
        result = eval(code, scope)
        if result is not None:
            await result
    finally:
        receiver.cancel()
        await asyncio.gather(receiver, return_exceptions=True)
        transport.close()


try:
    asyncio.run(main())
except ToolCallError as err:
    line = next(f.lineno for f in reversed(traceback.extract_tb(err.__traceback__))
                if f.filename == "<tool_weave>")
    sys.exit(f"<tool_weave>:{line}: {err.name}: {err}")
except Exception as err:
    report = traceback.TracebackException.from_exception(err)
    if not (any(f.filename == "<tool_weave>" for f in report.stack)
            or isinstance(err, SyntaxError) and err.filename == "<tool_weave>"):
        raise  # Runner failure: keep the original traceback.

    pending = [report]
    while pending:
        part = pending.pop()
        part.stack[:] = [f for f in part.stack if f.filename == "<tool_weave>"]
        pending.extend(e for e in (part.__cause__, part.__context__) if e is not None)
        pending.extend(getattr(part, "exceptions", None) or ())
    sys.stderr.writelines(report.format())
    sys.exit(1)
