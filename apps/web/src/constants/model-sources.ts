// Providers Memoh offers in the provider gallery, maintained by hand. Each entry
// has a stable id, how it is shown, and the client type and base URL a provider
// created from it uses. An entry with a source names the models.dev provider
// whose model list feeds the preview and the import. The logo always comes from
// models.dev, so every provider is drawn in the same style.
//
// Add a provider only after checking that its endpoint speaks the client type
// listed here. Providers that mix protocols, need a base URL with variables, or
// did not answer like an API when checked are left out until someone verifies
// them. The integrations Memoh implements itself (Codex, GitHub Copilot,
// OpenCode Go) and self-hosted gateways come from the former provider templates.
// A local server (Ollama, LM Studio) has no source even where models.dev lists
// one: it serves the models the user loaded, which only its endpoint knows.
// Azure OpenAI has none either: it is called by deployment name, which no
// catalog of model ids can supply.
export type ModelSourceClientType =
  | 'openai-completions'
  | 'openai-responses'
  | 'anthropic-messages'
  | 'google-generative-ai'
  | 'openai-codex'
  | 'github-copilot'
  | 'opencode-go'

export interface ModelSource {
  // Stable entry id; equals source for an entry taken from models.dev.
  id: string
  // models.dev provider id.
  source?: string
  name: string
  client_type: ModelSourceClientType
  // Empty for a client type that has a fixed endpoint.
  base_url: string
  // The models.dev provider whose logo to show, when it is not source (or id
  // for an entry without one). models.dev answers an id it has no logo for
  // with its generic logo, which keeps such entries in the same style.
  logo?: string
  // The models.dev logo carries its own colors; others are drawn in one color
  // and take the text color.
  colored_logo?: boolean
  // A subscription the user signs into and already pays for. The gallery lists
  // these first, and a provider made with its client type counts as it.
  subscription?: boolean
}

export const MODEL_SOURCES: readonly ModelSource[] = [
  { id: '302ai', source: '302ai', name: '302.AI', client_type: 'openai-completions', base_url: 'https://api.302.ai/v1', colored_logo: true },
  { id: 'abacus', source: 'abacus', name: 'Abacus', client_type: 'openai-completions', base_url: 'https://routellm.abacus.ai/v1' },
  { id: 'abliteration-ai', source: 'abliteration-ai', name: 'abliteration.ai', client_type: 'openai-completions', base_url: 'https://api.abliteration.ai/v1' },
  { id: 'above', source: 'above', name: 'above.dev', client_type: 'openai-completions', base_url: 'https://api.above.dev/v1' },
  { id: 'agnes', source: 'agnes', name: 'Agnes AI', client_type: 'openai-completions', base_url: 'https://apihub.agnes-ai.com/v1' },
  { id: 'ai-router', source: 'ai-router', name: 'AI-ROUTER', client_type: 'openai-completions', base_url: 'https://api.ai-router.dev/v1' },
  { id: 'ai21', source: 'ai21', name: 'AI21 Labs', client_type: 'openai-completions', base_url: 'https://api.ai21.com/studio/v1' },
  { id: 'aiand', source: 'aiand', name: 'ai&', client_type: 'openai-completions', base_url: 'https://api.aiand.com/v1' },
  { id: 'aihubmix', source: 'aihubmix', name: 'AIHubMix', client_type: 'openai-completions', base_url: 'https://aihubmix.com/v1' },
  { id: 'aimlapi', source: 'aimlapi', name: 'AI/ML API', client_type: 'openai-completions', base_url: 'https://api.aimlapi.com/v1' },
  { id: 'ainetcafe', source: 'ainetcafe', name: 'ainetcafe', client_type: 'openai-completions', base_url: 'https://microquickjs.com/v1' },
  { id: 'aixy', source: 'aixy', name: 'Aixy', client_type: 'openai-responses', base_url: 'https://api.aixy-gateway.com/v1' },
  { id: 'aki-io', source: 'aki-io', name: 'AKI.IO', client_type: 'openai-completions', base_url: 'https://aki.io/v1' },
  { id: 'alibaba', source: 'alibaba', name: 'Alibaba', client_type: 'openai-completions', base_url: 'https://dashscope-intl.aliyuncs.com/compatible-mode/v1' },
  { id: 'alibaba-cn', source: 'alibaba-cn', name: 'Aliyun Bailian', client_type: 'openai-completions', base_url: 'https://dashscope.aliyuncs.com/compatible-mode/v1' },
  { id: 'alibaba-coding-plan', source: 'alibaba-coding-plan', name: 'Alibaba Coding Plan', client_type: 'openai-completions', base_url: 'https://coding-intl.dashscope.aliyuncs.com/v1' },
  { id: 'alibaba-coding-plan-cn', source: 'alibaba-coding-plan-cn', name: 'Alibaba Coding Plan (China)', client_type: 'openai-completions', base_url: 'https://coding.dashscope.aliyuncs.com/v1' },
  { id: 'alibaba-token-plan', source: 'alibaba-token-plan', name: 'Alibaba Token Plan', client_type: 'openai-completions', base_url: 'https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1' },
  { id: 'alibaba-token-plan-cn', source: 'alibaba-token-plan-cn', name: 'Alibaba Token Plan (China)', client_type: 'openai-completions', base_url: 'https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1' },
  { id: 'ambient', source: 'ambient', name: 'Ambient', client_type: 'openai-completions', base_url: 'https://api.ambient.xyz/v1' },
  { id: 'amd', source: 'amd', name: 'AMD', client_type: 'openai-completions', base_url: 'https://developer.amd.com.cn/radeon/api/v1' },
  { id: 'anthropic', source: 'anthropic', name: 'Anthropic', client_type: 'anthropic-messages', base_url: 'https://api.anthropic.com/v1' },
  { id: 'anyapi', source: 'anyapi', name: 'AnyAPI', client_type: 'openai-completions', base_url: 'https://api.anyapi.ai/v1' },
  { id: 'arcee', source: 'arcee', name: 'Arcee', client_type: 'openai-completions', base_url: 'https://api.arcee.ai/api/v1' },
  { id: 'auriko', source: 'auriko', name: 'Auriko', client_type: 'openai-completions', base_url: 'https://api.auriko.ai/v1' },
  { id: 'azure-openai', name: 'Azure OpenAI', client_type: 'openai-completions', base_url: 'https://<resource>.openai.azure.com/openai/v1', logo: 'azure' },
  { id: 'bailing', source: 'bailing', name: 'Bailing', client_type: 'openai-completions', base_url: 'https://api.tbox.cn/api/llm/v1' },
  { id: 'baseten', source: 'baseten', name: 'Baseten', client_type: 'openai-completions', base_url: 'https://inference.baseten.co/v1' },
  { id: 'bee', source: 'bee', name: 'Bee by HEOSSI', client_type: 'openai-completions', base_url: 'https://api.bee.heossi.com/bee' },
  { id: 'berget', source: 'berget', name: 'Berget.AI', client_type: 'openai-completions', base_url: 'https://api.berget.ai/v1' },
  { id: 'blueclaw', source: 'blueclaw', name: 'Blue Claw', client_type: 'openai-completions', base_url: 'https://openai.blueclaw.network/v1' },
  { id: 'bothub', source: 'bothub', name: 'Bothub', client_type: 'openai-completions', base_url: 'https://openai.bothub.ru/v1' },
  { id: 'cerebras', source: 'cerebras', name: 'Cerebras', client_type: 'openai-completions', base_url: 'https://api.cerebras.ai/v1' },
  { id: 'claudinio', source: 'claudinio', name: 'Claudinio', client_type: 'openai-completions', base_url: 'https://api.claudin.io/v1' },
  { id: 'cline-pass', source: 'cline-pass', name: 'ClinePass', client_type: 'openai-completions', base_url: 'https://api.cline.bot/api/v1' },
  { id: 'cloudflare', source: 'cloudflare-workers-ai', name: 'Cloudflare Workers AI', client_type: 'openai-completions', base_url: 'https://api.cloudflare.com/client/v4/accounts/<account_id>/ai/v1' },
  { id: 'codex', name: 'OpenAI Codex', client_type: 'openai-codex', base_url: 'https://chatgpt.com/backend-api', logo: 'openai', subscription: true },
  { id: 'coralbricks', source: 'coralbricks', name: 'CoralBricks', client_type: 'openai-completions', base_url: 'https://inference.coralbricks.ai/v1' },
  { id: 'cortecs', source: 'cortecs', name: 'Cortecs', client_type: 'openai-completions', base_url: 'https://api.cortecs.ai/v1' },
  { id: 'crossmodel', source: 'crossmodel', name: 'CrossModel', client_type: 'openai-completions', base_url: 'https://api.crossmodel.ai/v1' },
  { id: 'crusoe', source: 'crusoe', name: 'Crusoe', client_type: 'openai-completions', base_url: 'https://api.inference.crusoecloud.com/v1' },
  { id: 'daoxe', source: 'daoxe', name: 'DaoXE', client_type: 'openai-completions', base_url: 'https://daoxe.com/v1' },
  { id: 'deepinfra', source: 'deepinfra', name: 'Deep Infra', client_type: 'openai-completions', base_url: 'https://api.deepinfra.com/v1/openai' },
  { id: 'deepseek', source: 'deepseek', name: 'DeepSeek', client_type: 'openai-completions', base_url: 'https://api.deepseek.com' },
  { id: 'digitalocean', source: 'digitalocean', name: 'DigitalOcean', client_type: 'openai-completions', base_url: 'https://inference.do-ai.run/v1' },
  { id: 'dinference', source: 'dinference', name: 'DInference', client_type: 'openai-completions', base_url: 'https://api.dinference.com/v1' },
  { id: 'drun', source: 'drun', name: 'D.Run (China)', client_type: 'openai-completions', base_url: 'https://chat.d.run/v1' },
  { id: 'ebcloud', source: 'ebcloud', name: 'EBCloud', client_type: 'openai-completions', base_url: 'https://maas-api.ebcloud.com/v1' },
  { id: 'echo', source: 'echo', name: 'Echo', client_type: 'openai-completions', base_url: 'https://echo.tracerml.ai/v1' },
  { id: 'edenai', source: 'edenai', name: 'Eden AI', client_type: 'openai-completions', base_url: 'https://api.edenai.run/v3' },
  { id: 'empiriolabs', source: 'empiriolabs', name: 'EmpirioLabs AI', client_type: 'openai-completions', base_url: 'https://api.empiriolabs.ai/v1' },
  { id: 'engy', source: 'engy', name: 'engy', client_type: 'openai-completions', base_url: 'https://api.engy.ai/v1' },
  { id: 'evroc', source: 'evroc', name: 'evroc', client_type: 'openai-completions', base_url: 'https://models.think.evroc.com/v1' },
  { id: 'fastrouter', source: 'fastrouter', name: 'FastRouter', client_type: 'openai-completions', base_url: 'https://go.fastrouter.ai/api/v1' },
  { id: 'fireworks-ai', source: 'fireworks-ai', name: 'Fireworks AI', client_type: 'openai-completions', base_url: 'https://api.fireworks.ai/inference/v1' },
  { id: 'friendli', source: 'friendli', name: 'Friendli', client_type: 'openai-completions', base_url: 'https://api.friendli.ai/serverless/v1' },
  { id: 'frogbot', source: 'frogbot', name: 'FrogBot', client_type: 'openai-completions', base_url: 'https://app.frogbot.ai/api/v1', colored_logo: true },
  { id: 'github-copilot', name: 'GitHub Copilot', client_type: 'github-copilot', base_url: '', logo: 'github-copilot', subscription: true },
  { id: 'gitlab', source: 'gitlab', name: 'GitLab Duo', client_type: 'openai-completions', base_url: 'https://gitlab.com/api/v4' },
  { id: 'google', source: 'google', name: 'Google', client_type: 'google-generative-ai', base_url: 'https://generativelanguage.googleapis.com/v1beta' },
  { id: 'groq', source: 'groq', name: 'Groq', client_type: 'openai-completions', base_url: 'https://api.groq.com/openai/v1' },
  { id: 'hpc-ai', source: 'hpc-ai', name: 'HPC-AI', client_type: 'openai-completions', base_url: 'https://api.hpc-ai.com/inference/v1', colored_logo: true },
  { id: 'huggingface', source: 'huggingface', name: 'HuggingFace', client_type: 'openai-completions', base_url: 'https://router.huggingface.co/v1' },
  { id: 'hyper', source: 'hyper', name: 'Charm Hyper', client_type: 'openai-completions', base_url: 'https://hyper.charm.land/v1' },
  { id: 'iflowcn', source: 'iflowcn', name: 'iFlow', client_type: 'openai-completions', base_url: 'https://apis.iflow.cn/v1' },
  { id: 'impossibl', source: 'impossibl', name: 'Impossibl', client_type: 'openai-completions', base_url: 'https://api.impossibl.com/v1' },
  { id: 'inception', source: 'inception', name: 'Inception', client_type: 'openai-completions', base_url: 'https://api.inceptionlabs.ai/v1' },
  { id: 'inceptron', source: 'inceptron', name: 'Inceptron', client_type: 'openai-completions', base_url: 'https://api.inceptron.io/v1' },
  { id: 'inco', source: 'inco', name: 'Inco', client_type: 'openai-completions', base_url: 'https://api.inco.ai/v1' },
  { id: 'infer', source: 'infer', name: 'Infer by Flow7', client_type: 'openai-responses', base_url: 'https://infer.flow7.org/v1' },
  { id: 'io-net', source: 'io-net', name: 'IO.NET', client_type: 'openai-completions', base_url: 'https://api.intelligence.io.solutions/api/v1' },
  { id: 'iteracompute', source: 'iteracompute', name: 'IteraCompute', client_type: 'openai-completions', base_url: 'https://api.iteracompute.com/v1' },
  { id: 'jalapeno', source: 'jalapeno', name: 'Jalapeno Cloud', client_type: 'openai-completions', base_url: 'https://api.jalapeno-cloud.ai/v1' },
  { id: 'jiekou', source: 'jiekou', name: 'Jiekou.AI', client_type: 'openai-completions', base_url: 'https://api.jiekou.ai/openai' },
  { id: 'kimi-code-plan-cn', source: 'kimi-code-plan-cn', name: 'Kimi For Coding (kimi.com)', client_type: 'openai-completions', base_url: 'https://api.kimi.com/coding/v1' },
  { id: 'kimi-code-plan-global', source: 'kimi-code-plan-global', name: 'Kimi For Coding (kimi.ai)', client_type: 'openai-completions', base_url: 'https://api.kimi.ai/coding/v1' },
  { id: 'klokintegration', source: 'klokintegration', name: 'klokintegration.se', client_type: 'openai-completions', base_url: 'https://api-gw.klok.ipaas.se/proxy/kloker-key/v1' },
  { id: 'kosmik', source: 'kosmik', name: 'Kosmik Compute', client_type: 'openai-completions', base_url: 'https://api.koscompute.com/v1' },
  { id: 'kuae-cloud-coding-plan', source: 'kuae-cloud-coding-plan', name: 'KUAE Cloud Coding Plan', client_type: 'openai-completions', base_url: 'https://coding-plan-endpoint.kuaecloud.net/v1' },
  { id: 'llama', source: 'llama', name: 'Llama', client_type: 'openai-completions', base_url: 'https://api.llama.com/compat/v1' },
  { id: 'llmgateway', source: 'llmgateway', name: 'DevPass (LLM Gateway)', client_type: 'openai-completions', base_url: 'https://api.llmgateway.io/v1' },
  { id: 'llmgateway-providers', source: 'llmgateway-providers', name: 'LLM Gateway', client_type: 'openai-completions', base_url: 'https://api.llmgateway.io/v1' },
  { id: 'llmtech', source: 'llmtech', name: 'LLM Tech', client_type: 'openai-completions', base_url: 'https://api.llmtech.eu/v1' },
  { id: 'llmtr', source: 'llmtr', name: 'LLMTR', client_type: 'openai-completions', base_url: 'https://llmtr.com/v1' },
  { id: 'lmstudio', name: 'LM Studio', client_type: 'openai-completions', base_url: 'http://127.0.0.1:1234/v1', logo: 'lmstudio' },
  { id: 'longcat', source: 'longcat', name: 'LongCat', client_type: 'openai-completions', base_url: 'https://api.longcat.chat/openai' },
  { id: 'lowrouter', source: 'lowrouter', name: 'LowRouter', client_type: 'openai-completions', base_url: 'https://api.lowrouter.ai/v1' },
  { id: 'lucidquery', source: 'lucidquery', name: 'LucidQuery', client_type: 'openai-completions', base_url: 'https://api.lucidquery.com/v1' },
  { id: 'meganova', source: 'meganova', name: 'Meganova', client_type: 'openai-completions', base_url: 'https://api.meganova.ai/v1' },
  { id: 'melious', source: 'melious', name: 'Melious', client_type: 'openai-completions', base_url: 'https://api.melious.ai/v1' },
  { id: 'merge-gateway', source: 'merge-gateway', name: 'Merge Gateway', client_type: 'openai-completions', base_url: 'https://api-gateway.merge.dev/v1/ai-sdk' },
  { id: 'meta', source: 'meta', name: 'Meta', client_type: 'openai-responses', base_url: 'https://api.meta.ai/v1' },
  { id: 'minimax', source: 'minimax', name: 'MiniMax', client_type: 'openai-completions', base_url: 'https://api.minimaxi.com/v1' },
  { id: 'minimax-cn', source: 'minimax-cn', name: 'MiniMax (minimax.cn)', client_type: 'anthropic-messages', base_url: 'https://api.minimax.cn/anthropic/v1' },
  { id: 'minimax-cn-coding-plan', source: 'minimax-cn-coding-plan', name: 'MiniMax Token Plan (minimax.cn)', client_type: 'anthropic-messages', base_url: 'https://api.minimax.cn/anthropic/v1' },
  { id: 'minimax-coding-plan', source: 'minimax-coding-plan', name: 'MiniMax Token Plan (minimax.io)', client_type: 'anthropic-messages', base_url: 'https://api.minimax.io/anthropic/v1' },
  { id: 'mistral', source: 'mistral', name: 'Mistral', client_type: 'openai-completions', base_url: 'https://api.mistral.ai/v1' },
  { id: 'mixlayer', source: 'mixlayer', name: 'Mixlayer', client_type: 'openai-completions', base_url: 'https://models.mixlayer.ai/v1' },
  { id: 'moark', source: 'moark', name: 'Moark', client_type: 'openai-completions', base_url: 'https://moark.com/v1' },
  { id: 'modal', source: 'modal', name: 'Modal', client_type: 'openai-completions', base_url: 'https://inference.us-west.modal.direct/v1' },
  { id: 'model-oracle-ai', source: 'model-oracle-ai', name: 'Model Oracle AI', client_type: 'openai-completions', base_url: 'https://api.modeloracle.com/api/v1' },
  { id: 'modelis', source: 'modelis', name: 'Modelis', client_type: 'openai-completions', base_url: 'https://modelishub.com/v1' },
  { id: 'modelscope', source: 'modelscope', name: 'ModelScope', client_type: 'openai-completions', base_url: 'https://api-inference.modelscope.cn/v1' },
  { id: 'moonshotai', source: 'moonshotai', name: 'Moonshot AI', client_type: 'openai-completions', base_url: 'https://api.moonshot.ai/v1' },
  { id: 'moonshotai-cn', source: 'moonshotai-cn', name: 'Moonshot', client_type: 'openai-completions', base_url: 'https://api.moonshot.cn/v1' },
  { id: 'morph', source: 'morph', name: 'Morph', client_type: 'openai-completions', base_url: 'https://api.morphllm.com/v1' },
  { id: 'nan', source: 'nan', name: 'NaN', client_type: 'openai-completions', base_url: 'https://api.nan.builders/v1' },
  { id: 'nano-gpt', source: 'nano-gpt', name: 'NanoGPT', client_type: 'openai-completions', base_url: 'https://nano-gpt.com/api/v1' },
  { id: 'nearai', source: 'nearai', name: 'NEAR AI Cloud', client_type: 'openai-completions', base_url: 'https://cloud-api.near.ai/v1' },
  { id: 'nebius', source: 'nebius', name: 'Nebius Token Factory', client_type: 'openai-completions', base_url: 'https://api.tokenfactory.nebius.com/v1' },
  { id: 'neuralwatt', source: 'neuralwatt', name: 'Neuralwatt', client_type: 'openai-completions', base_url: 'https://api.neuralwatt.com/v1' },
  { id: 'newapi', name: 'New API', client_type: 'openai-completions', base_url: 'https://your.new-api-provider.com/v1' },
  { id: 'novita-ai', source: 'novita-ai', name: 'Novita AI', client_type: 'openai-completions', base_url: 'https://api.novita.ai/openai', colored_logo: true },
  { id: 'nvidia', source: 'nvidia', name: 'Nvidia', client_type: 'openai-completions', base_url: 'https://integrate.api.nvidia.com/v1' },
  { id: 'oci', source: 'oci', name: 'OCI Generative AI', client_type: 'openai-completions', base_url: 'https://inference.generativeai.us-chicago-1.oci.oraclecloud.com/openai/v1' },
  { id: 'ollama', name: 'Ollama', client_type: 'openai-completions', base_url: 'http://127.0.0.1:11434/v1', logo: 'ollama-cloud' },
  { id: 'ollama-cloud', source: 'ollama-cloud', name: 'Ollama Cloud', client_type: 'openai-completions', base_url: 'https://ollama.com/v1' },
  { id: 'openai', source: 'openai', name: 'OpenAI', client_type: 'openai-responses', base_url: 'https://api.openai.com/v1' },
  { id: 'opencode-go', source: 'opencode-go', name: 'OpenCode Go', client_type: 'opencode-go', base_url: 'https://opencode.ai/zen/go/v1', subscription: true },
  { id: 'openreason', source: 'openreason', name: 'OpenReason', client_type: 'openai-completions', base_url: 'https://api.openreason.app/v1' },
  { id: 'openrouter', source: 'openrouter', name: 'OpenRouter', client_type: 'openai-completions', base_url: 'https://openrouter.ai/api/v1' },
  { id: 'opper', source: 'opper', name: 'Opper', client_type: 'openai-completions', base_url: 'https://api.opper.ai/v3/compat' },
  { id: 'orcarouter', source: 'orcarouter', name: 'OrcaRouter', client_type: 'openai-completions', base_url: 'https://api.orcarouter.ai/v1', colored_logo: true },
  { id: 'pareto', source: 'pareto', name: 'Pareto Inference', client_type: 'openai-completions', base_url: 'https://api.paretoinference.com/v1' },
  { id: 'pendra', source: 'pendra', name: 'Pendra', client_type: 'openai-completions', base_url: 'https://api.pendra.ai/api/v1' },
  { id: 'perplexity', source: 'perplexity', name: 'Perplexity', client_type: 'openai-completions', base_url: 'https://api.perplexity.ai' },
  { id: 'perplexity-agent', source: 'perplexity-agent', name: 'Perplexity Agent', client_type: 'openai-responses', base_url: 'https://api.perplexity.ai/v1' },
  { id: 'pioneer', source: 'pioneer', name: 'Pioneer', client_type: 'openai-completions', base_url: 'https://api.fastino.ai/v1' },
  { id: 'poe', source: 'poe', name: 'Poe', client_type: 'openai-completions', base_url: 'https://api.poe.com/v1' },
  { id: 'poolside', source: 'poolside', name: 'Poolside', client_type: 'openai-completions', base_url: 'https://inference.poolside.ai/v1' },
  { id: 'prime', source: 'prime', name: 'Prime Inference', client_type: 'openai-completions', base_url: 'https://api.pinference.ai/api/v1' },
  { id: 'qihang-ai', source: 'qihang-ai', name: 'QiHang', client_type: 'openai-completions', base_url: 'https://api.qhaigc.net/v1' },
  { id: 'qiniu-ai', source: 'qiniu-ai', name: 'Qiniu', client_type: 'openai-completions', base_url: 'https://api.qnaigc.com/v1' },
  { id: 'regolo-ai', source: 'regolo-ai', name: 'Regolo AI', client_type: 'openai-completions', base_url: 'https://api.regolo.ai/v1', colored_logo: true },
  { id: 'requesty', source: 'requesty', name: 'Requesty', client_type: 'openai-completions', base_url: 'https://router.requesty.ai/v1' },
  { id: 'routing-run', source: 'routing-run', name: 'routing.run', client_type: 'openai-completions', base_url: 'https://api.routing.run/v1' },
  { id: 'runinfra', source: 'runinfra', name: 'RunInfra', client_type: 'openai-completions', base_url: 'https://api.runinfra.ai/v1' },
  { id: 'sarvam', source: 'sarvam', name: 'Sarvam AI', client_type: 'openai-completions', base_url: 'https://api.sarvam.ai/v1' },
  { id: 'scaleway', source: 'scaleway', name: 'Scaleway', client_type: 'openai-completions', base_url: 'https://api.scaleway.ai/v1' },
  { id: 'scnet-token-plan', source: 'scnet-token-plan', name: 'SCNet Token Plan', client_type: 'openai-completions', base_url: 'https://api.scnet.cn/api/llm/v1' },
  { id: 'scx-ai', source: 'scx-ai', name: 'SCX.ai', client_type: 'openai-completions', base_url: 'https://api.scx.ai/v1' },
  { id: 'sensenova', source: 'sensenova', name: 'SenseNova (China)', client_type: 'openai-completions', base_url: 'https://token.sensenova.cn/v1' },
  { id: 'siliconflow', source: 'siliconflow', name: 'SiliconFlow', client_type: 'openai-completions', base_url: 'https://api.siliconflow.com/v1' },
  { id: 'siliconflow-cn', source: 'siliconflow-cn', name: 'SiliconFlow (China)', client_type: 'openai-completions', base_url: 'https://api.siliconflow.cn/v1' },
  { id: 'standardcompute', source: 'standardcompute', name: 'Standard Compute', client_type: 'openai-completions', base_url: 'https://api.stdcmpt.com/v1' },
  { id: 'stepfun', source: 'stepfun', name: 'StepFun (China)', client_type: 'openai-completions', base_url: 'https://api.stepfun.com/v1' },
  { id: 'stepfun-ai', source: 'stepfun-ai', name: 'StepFun (Global)', client_type: 'openai-completions', base_url: 'https://api.stepfun.ai/v1' },
  { id: 'stepfun-ai-step-plan', source: 'stepfun-ai-step-plan', name: 'StepFun Step Plan (Global)', client_type: 'openai-completions', base_url: 'https://api.stepfun.ai/step_plan/v1' },
  { id: 'stepfun-step-plan', source: 'stepfun-step-plan', name: 'StepFun Step Plan (China)', client_type: 'openai-completions', base_url: 'https://api.stepfun.com/step_plan/v1' },
  { id: 'subconscious', source: 'subconscious', name: 'Subconscious', client_type: 'anthropic-messages', base_url: 'https://api.subconscious.dev/v1' },
  { id: 'submodel', source: 'submodel', name: 'submodel', client_type: 'openai-completions', base_url: 'https://llm.submodel.ai/v1' },
  { id: 'synthetic', source: 'synthetic', name: 'Synthetic', client_type: 'openai-completions', base_url: 'https://api.synthetic.new/openai/v1' },
  { id: 'tempr', source: 'tempr', name: 'Tempr Gateway', client_type: 'openai-completions', base_url: 'https://api.temprhq.io/v1' },
  { id: 'tencent-coding-plan', source: 'tencent-coding-plan', name: 'Tencent Coding Plan (China)', client_type: 'openai-completions', base_url: 'https://api.lkeap.cloud.tencent.com/coding/v3' },
  { id: 'tencent-token-plan', source: 'tencent-token-plan', name: 'Tencent Token Plan', client_type: 'openai-completions', base_url: 'https://api.lkeap.cloud.tencent.com/plan/v3' },
  { id: 'tencent-tokenhub', source: 'tencent-tokenhub', name: 'Tencent TokenHub', client_type: 'openai-completions', base_url: 'https://tokenhub.tencentmaas.com/v1' },
  { id: 'tensorx', source: 'tensorx', name: 'TensorX', client_type: 'openai-completions', base_url: 'https://api.tensorx.ai/v1' },
  { id: 'the-grid-ai', source: 'the-grid-ai', name: 'The Grid AI', client_type: 'openai-completions', base_url: 'https://api.thegrid.ai/v1' },
  { id: 'thinkingmachines', source: 'thinkingmachines', name: 'Thinking Machines', client_type: 'anthropic-messages', base_url: 'https://tinker.thinkingmachines.dev/services/tinker-prod/anthropic/api/v1' },
  { id: 'tinfoil', source: 'tinfoil', name: 'Tinfoil', client_type: 'openai-completions', base_url: 'https://inference.tinfoil.sh/v1' },
  { id: 'togetherai', source: 'togetherai', name: 'Together AI', client_type: 'openai-completions', base_url: 'https://api.together.xyz/v1' },
  { id: 'tokengo', source: 'tokengo', name: 'TokenGo', client_type: 'openai-completions', base_url: 'https://api.tokengo.com/v1' },
  { id: 'tokenrouter', source: 'tokenrouter', name: 'TokenRouter', client_type: 'openai-completions', base_url: 'https://api.tokenrouter.com/v1' },
  { id: 'trustedrouter', source: 'trustedrouter', name: 'TrustedRouter', client_type: 'openai-completions', base_url: 'https://api.trustedrouter.com/v1' },
  { id: 'umans-ai', source: 'umans-ai', name: 'Umans AI', client_type: 'openai-completions', base_url: 'https://api.code.umans.ai/v1', colored_logo: true },
  { id: 'umans-ai-coding-plan', source: 'umans-ai-coding-plan', name: 'Umans AI Coding Plan', client_type: 'openai-completions', base_url: 'https://api.code.umans.ai/v1', colored_logo: true },
  { id: 'unorouter', source: 'unorouter', name: 'UnoRouter', client_type: 'openai-completions', base_url: 'https://api.unorouter.com/v1' },
  { id: 'upstage', source: 'upstage', name: 'Upstage', client_type: 'openai-completions', base_url: 'https://api.upstage.ai/v1/solar' },
  { id: 'vancine', source: 'vancine', name: 'Vancine', client_type: 'openai-completions', base_url: 'https://vancine.com/v1' },
  { id: 'venice', source: 'venice', name: 'Venice AI', client_type: 'openai-completions', base_url: 'https://api.venice.ai/api/v1' },
  { id: 'vispark', source: 'vispark', name: 'Vispark', client_type: 'openai-completions', base_url: 'https://api.lab.vispark.in/v1' },
  { id: 'volcengine', source: 'volcengine', name: 'Volcengine Ark', client_type: 'openai-completions', base_url: 'https://ark.cn-beijing.volces.com/api/v3' },
  { id: 'volcengine-coding-plan', source: 'volcengine-coding-plan', name: 'Volcengine Ark Coding Plan', client_type: 'openai-completions', base_url: 'https://ark.cn-beijing.volces.com/api/coding/v3' },
  { id: 'vultr', source: 'vultr', name: 'Vultr', client_type: 'openai-completions', base_url: 'https://api.vultrinference.com/v1' },
  { id: 'wafer.ai', source: 'wafer.ai', name: 'Wafer', client_type: 'openai-completions', base_url: 'https://pass.wafer.ai/v1', colored_logo: true },
  { id: 'wallaby', source: 'wallaby', name: 'Wallaby', client_type: 'openai-completions', base_url: 'https://api.wallabytoken.com/v1' },
  { id: 'wandb', source: 'wandb', name: 'CoreWeave', client_type: 'openai-completions', base_url: 'https://api.inference.wandb.ai/v1' },
  { id: 'xai', source: 'xai', name: 'xAI (Grok)', client_type: 'openai-responses', base_url: 'https://api.x.ai/v1' },
  { id: 'xiaomi', source: 'xiaomi', name: 'Xiaomi', client_type: 'openai-completions', base_url: 'https://api.xiaomimimo.com/v1' },
  { id: 'xiaomi-token-plan-ams', source: 'xiaomi-token-plan-ams', name: 'Xiaomi Token Plan (Europe)', client_type: 'openai-completions', base_url: 'https://token-plan-ams.xiaomimimo.com/v1' },
  { id: 'xiaomi-token-plan-cn', source: 'xiaomi-token-plan-cn', name: 'Xiaomi Token Plan (China)', client_type: 'openai-completions', base_url: 'https://token-plan-cn.xiaomimimo.com/v1' },
  { id: 'xiaomi-token-plan-sgp', source: 'xiaomi-token-plan-sgp', name: 'Xiaomi Token Plan (Singapore)', client_type: 'openai-completions', base_url: 'https://token-plan-sgp.xiaomimimo.com/v1' },
  { id: 'xpersona', source: 'xpersona', name: 'Xpersona', client_type: 'openai-completions', base_url: 'https://www.xpersona.co/v1' },
  { id: 'zai', source: 'zai', name: 'Z.AI GLM', client_type: 'openai-completions', base_url: 'https://api.z.ai/api/paas/v4' },
  { id: 'zeldoc', source: 'zeldoc', name: 'Zeldoc', client_type: 'openai-completions', base_url: 'https://api.zeldoc.ai/v1', colored_logo: true },
  { id: 'zhipuai', source: 'zhipuai', name: 'Zhipu AI', client_type: 'openai-completions', base_url: 'https://open.bigmodel.cn/api/paas/v4' },
  { id: 'zhipuai-coding-plan', source: 'zhipuai-coding-plan', name: 'Zhipu AI Coding Plan', client_type: 'openai-completions', base_url: 'https://open.bigmodel.cn/api/coding/paas/v4' },
]

const byId = new Map(MODEL_SOURCES.map(item => [item.id, item]))
const bySource = new Map(MODEL_SOURCES.filter(item => item.source).map(item => [item.source!, item]))

export function findModelSource(id: string | null | undefined): ModelSource | undefined {
  return id ? byId.get(id) : undefined
}

// The entry whose models.dev provider is source.
export function findModelSourceBySource(source: string | null | undefined): ModelSource | undefined {
  return source ? bySource.get(source) : undefined
}

// Splits gallery entries into subscriptions and every other provider, each in
// the order given.
export function groupModelSources(items: readonly ModelSource[]) {
  const subscriptions: ModelSource[] = []
  const others: ModelSource[] = []
  for (const item of items) {
    (item.subscription ? subscriptions : others).push(item)
  }
  return { subscriptions, others }
}

// Each subscription's client type belongs to it alone (a test holds this).
const subscriptionByClientType = new Map(MODEL_SOURCES.filter(item => item.subscription).map(item => [item.client_type as string, item]))

// The subscription a provider made with a subscription's client type is, when
// it also points at that subscription: no base URL (the integration's fixed
// endpoint) or the entry's own. One aimed at a proxy or another gateway is a
// custom provider that happens to speak the protocol.
export function subscriptionForProvider(clientType: string | null | undefined, baseUrl: unknown): ModelSource | undefined {
  const entry = subscriptionByClientType.get(clientType ?? '')
  if (!entry) return undefined
  const base = normalizedBaseUrl(baseUrl)
  return !entry.base_url || !base || base === normalizedBaseUrl(entry.base_url) ? entry : undefined
}

function normalizedBaseUrl(value: unknown): string {
  return typeof value === 'string' ? value.trim().replace(/\/+$/, '').toLowerCase() : ''
}

// The logo models.dev serves for an entry; an id without a logo of its own gets
// models.dev's generic one.
export function modelSourceLogo(item: ModelSource): string {
  return `https://models.dev/logos/${encodeURIComponent(item.logo ?? item.source ?? item.id)}.svg`
}

// Former provider template keys and the entry that replaced each. A provider
// created from a template counts as configured for its entry.
export const TEMPLATE_SOURCES: Readonly<Record<string, string>> = {
  'anthropic': 'anthropic',
  'azure-openai': 'azure-openai',
  'cerebras': 'cerebras',
  'cloudflare': 'cloudflare',
  'codex': 'codex',
  'deepseek': 'deepseek',
  'fireworks': 'fireworks-ai',
  'github-copilot': 'github-copilot',
  'google': 'google',
  'groq': 'groq',
  'huggingface': 'huggingface',
  'lmstudio': 'lmstudio',
  'minimax': 'minimax',
  'mistral': 'mistral',
  'moonshot': 'moonshotai-cn',
  'newapi': 'newapi',
  'ollama': 'ollama',
  'openai': 'openai',
  'opencode-go': 'opencode-go',
  'openrouter': 'openrouter',
  'perplexity': 'perplexity',
  'qwen': 'alibaba-cn',
  'together': 'togetherai',
  'xai': 'xai',
  'zai': 'zai',
  'zhipu': 'zhipuai',
}

// The models.dev logo for a former template or preset key, through the entry
// that replaced it.
export function modelSourceLogoForKey(key: string): string | undefined {
  const entry = findModelSource(TEMPLATE_SOURCES[key] ?? key)
  return entry ? modelSourceLogo(entry) : undefined
}

// A local endpoint usually runs without an API key, and the OAuth integrations
// sign in instead.
export function modelSourceNeedsApiKey(item: ModelSource): boolean {
  if (item.client_type === 'openai-codex' || item.client_type === 'github-copilot') return false
  try {
    const host = new URL(item.base_url).hostname
    return host !== 'localhost' && host !== '127.0.0.1'
  } catch {
    return true
  }
}

const MODELS_DEV_LOGO = /^https:\/\/models\.dev\/logos\/([^/]+)\.svg$/

// Whether an icon is a models.dev logo drawn in one color, which should take the
// text color rather than render as an image.
export function isMonochromeLogo(icon: string | null | undefined): boolean {
  const match = icon ? MODELS_DEV_LOGO.exec(icon) : null
  if (!match) return false
  return !findModelSourceBySource(decodeURIComponent(match[1]!))?.colored_logo
}

interface ProviderLike {
  id?: string
  client_type?: string
  icon?: string
  config?: Record<string, unknown>
  metadata?: Record<string, unknown>
}

// The logo to show for a provider: its entry's models.dev logo. A provider
// created before the gallery may carry an icon from the former local icon set;
// that is ignored so every provider is drawn the same way.
export function providerLogo(provider: ProviderLike | null | undefined): string | undefined {
  const entry = providerGalleryEntry(provider)
  if (entry) return modelSourceLogo(entry)
  const icon = provider?.icon
  return icon && MODELS_DEV_LOGO.test(icon) ? icon : undefined
}

// A provider not yet created, for the gallery's draft detail. Its metadata
// records the entry and, when there is one, the models.dev source.
export function providerDraftFromSource(item: ModelSource) {
  return {
    name: item.name,
    client_type: item.client_type,
    icon: modelSourceLogo(item),
    enable: false,
    config: item.base_url ? { base_url: item.base_url } : {},
    metadata: providerSourceMetadata(item),
  }
}

export function providerSourceMetadata(item: ModelSource): Record<string, unknown> {
  return { catalog: item.id, ...(item.source && { source: item.source }) }
}

function recordedSource(provider: ProviderLike | null | undefined): string | undefined {
  const source = provider?.metadata?.source
  return typeof source === 'string' && source ? source : undefined
}

// The models.dev source whose catalog a provider imports from: recorded on the
// provider, or the source of the entry it was created from, which covers
// providers made from a former template or preset.
export function providerSource(provider: ProviderLike | null | undefined): string | undefined {
  return recordedSource(provider) ?? providerCatalogEntry(provider)?.source
}

// The entry a provider was created from: recorded on the provider, or the
// former template or preset it came from.
// An entry id a later release dropped falls through to the source and the
// former template, so the provider keeps its logo and stays configured.
export function providerCatalogEntry(provider: ProviderLike | null | undefined): ModelSource | undefined {
  const metadata = provider?.metadata ?? {}
  const recorded = typeof metadata.catalog === 'string' ? findModelSource(metadata.catalog) : undefined
  if (recorded) return recorded
  const bySource = findModelSourceBySource(recordedSource(provider))
  if (bySource) return bySource
  for (const section of ['preset', 'registry']) {
    const legacy = (metadata[section] as { source?: unknown } | undefined)?.source
    if (typeof legacy === 'string') {
      const id = TEMPLATE_SOURCES[legacy.replace(/\.yaml$/, '')]
      if (id) return findModelSource(id)
    }
  }
  return undefined
}

// The entry the gallery counts a provider as: the one it records, or for a
// provider made in the custom form with a subscription's client type and
// endpoint before that form recorded the entry, that subscription, so it is not
// offered again.
// Only the gallery and the logo read this; what such an older provider
// imports from stays its own record, where the server reports the source of
// an older OpenCode Go subscription on its official endpoint.
export function providerGalleryEntry(provider: ProviderLike | null | undefined): ModelSource | undefined {
  return providerCatalogEntry(provider) ?? subscriptionForProvider(provider?.client_type, provider?.config?.base_url)
}
