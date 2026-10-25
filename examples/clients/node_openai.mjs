// Using LLMGW from the OpenAI Node.js SDK.
//
//   npm install openai
//   LLMGW_URL=http://127.0.0.1:8080 LLMGW_KEY=gw-alice-change-me node node_openai.mjs
import OpenAI from "openai";

const client = new OpenAI({
  baseURL: (process.env.LLMGW_URL ?? "http://127.0.0.1:8080") + "/v1",
  apiKey: process.env.LLMGW_KEY,
  defaultHeaders: { "X-LLMGW-Client": "node-example" },
});

const reply = await client.chat.completions.create({
  model: "mistral-small-latest",
  messages: [
    { role: "user", content: "Write a polite reminder to ACME SA about invoice TCK-104233, contact jean.martin@acme.be." },
  ],
});
console.log(reply.choices[0].message.content);

// What the gateway lets this key do.
const me = await fetch(`${client.baseURL}/me`, {
  headers: { Authorization: `Bearer ${process.env.LLMGW_KEY}` },
}).then((r) => r.json());
console.log(`profile ${me.profile}, ${me.usage.DayReq} requests today`);
