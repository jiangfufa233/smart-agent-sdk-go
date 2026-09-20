# Optional fixture generation only: Python 3 with tiktoken==0.12.0.
import importlib.metadata
import itertools
import json
from pathlib import Path
import random
import tiktoken

if importlib.metadata.version("tiktoken") != "0.12.0":
    raise RuntimeError("fixture generation requires tiktoken==0.12.0")

encoding = tiktoken.get_encoding("cl100k_base")
texts = [
    "", "hello world", "antidisestablishmentarianism", "2 + 2 = 4",
    "お誕生日おめでとう", "你好，世界！", "مرحبا بالعالم", "Привет, мир!",
    "can't I'VE we'll THEY'RE isn't 's 'S", "e\u0301 é Å A\u030a",
    "<|endoftext|><|fim_prefix|><|fim_middle|><|fim_suffix|><|endofprompt|>",
    "func main() {\n\tfmt.Println(\"你好\")\n}\n", "0001234567890123456789",
    "a" * 1024, " " * 512, "\r\n" * 128,
    "\x00\x01\x1c\x1d\x1e\x1f\x7f\u0085\u00a0\u1680\u2028\u2029\u202f\u205f\u3000",
    "\u0130\u0131\u017f\u212a\U0001f600\U0001f3fd\u200d\U0001f4bb",
]
texts += ["a" + chr(i) + "b" for i in range(256)]
whitespace = [" ", "\t", "\r", "\n", "\v", "\f", "\u0085", "\u00a0", "\u2028", "\u3000"]
texts += ["left" + "".join(parts) + "right" for parts in itertools.product(whitespace, repeat=2)]
rng = random.Random(233)
alphabet = "abcXYZ0123456789!?.'\"{}[]_/-=+ \t\r\n你好世界日本語éÅΣςαβمرحبا\u0301\u00a0\u200d\U0001f600"
texts += ["".join(rng.choices(alphabet, k=rng.randrange(1, 120))) for _ in range(512)]
fixture = {
    "reference": "tiktoken==0.12.0, cl100k_base, encode_ordinary",
    "vocabulary_sha256": "223921b76ee99bde995b7ff738513eef100fb51d18c93597a113bcffe865b2a7",
    "vectors": [{"text": text, "tokens": len(encoding.encode_ordinary(text))} for text in texts],
}
path = Path(__file__).with_name("cl100k.json")
path.write_text(json.dumps(fixture, ensure_ascii=True, indent=2) + "\n")
print(f"Generated {len(texts)} reference vectors in {path}")
