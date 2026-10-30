# 写一个供应商包

[English](PROVIDER-TEMPLATE.en.md) | 简体中文

更新:2026-10-31。这份文档对应仓库里**两个能跑的包**,不是纸上约定:

- `magpieplugin/testdata/openai-compatible/` —— Chat Completions 方言
- `magpieplugin/testdata/anthropic-compatible/` —— Messages 方言(比上面多声明两样东西)

它们被 `magpieplugin/template_test.go` 直接加载并驱动,所以不会悄悄过期。

## 一个包要提供什么

```js
export async function Plugin(input) {
  return {
    auth: {
      provider: "my-vendor",              // 账号存哪个 id 下,必须稳定
      methods: [ /* 登录方式,见下 */ ],
      async loader(getAuth) {             // 宿主靠它发请求
        const auth = await getAuth()      // 上次存下的登录信息
        return {
          baseURL: "https://api.example.com/v1",
          dialect: "chat",                // chat | responses | messages
          headers: { "x-api-key": auth.key }, // 可选:供应商自己的头名
          fetch: (url, init) => fetch(url, init),
        }
      },
      // usage / checkin 是可选的:缺了就是「供应商没报」,不是 0
    },
  }
}
```

登录方式里最关键的一点:**密钥类登录当场返回结果**

```js
async authorize(inputs) {
  return { type: "success", key: inputs.apiKey, metadata: { baseURL: inputs.baseURL } }
}
```

返回 `type: "success"` 时宿主不会去轮询一个不存在的 callback;`metadata` 是留给 `loader` 的长期信息(例如 base URL),它会随登录信息一起被存下来。

## 请求路径由方言决定

| `dialect` | 默认路径 | 用途 |
|---|---|---|
| `chat` | `/chat/completions` | OpenAI Chat Completions |
| `responses` | `/responses` | OpenAI Responses |
| `messages` | `/messages` | Anthropic Messages |

供应商路径不同就在 loader 里给 `path`。**包不需要构造请求体、也不需要解析回复** —— 这两件事宿主按方言做,供应商的差异只留在包里。

## 装到客户端里

把这两个文件放在同一层,压成 zip(不要在 `package.json` 外面再套一层目录):

```
my-vendor.zip
├── package.json
└── index.mjs
```

iOS 客户端用「安装插件包」选这个 zip 即可;Core 会解包、剥掉多余的一层目录、拒绝符号链接与越界路径,再加载一次确认它真的能跑(不能跑的包不会留下记录)。

`TestTemplateSurvivesBeingZipped` 就是这条路径的测试:把模板压成 zip、解包、加载、读 provider。

## 自己验一遍

```sh
go test -run 'TestTemplate|TestAnthropic' -v ./magpieplugin
```

要加自己的一份,把 `testdata/openai-compatible/` 复制成 `testdata/<你的名字>/`,改掉 `provider`,再照 `template_test.go` 写一条最小的测试。**让它先失败**:把 `auth.provider` 改成别的值,确认测试会红,再改回来 —— 这一步能区分"测试通过"和"测试没在测"。

## 不该做的事

- 不要 `require`/`import` npm 包或 Node 内置模块(`fs`、`crypto`、`child_process` 等):本运行时在加载阶段就拒绝,并且会指名是哪一个
- 不要自己拼 SSE 或解析分块:宿主已经做了,自己拼会得到两套行为
- 不要在没有数据时编造用量或模型列表:"供应商没说"和"0"在界面上是两件事
