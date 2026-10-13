// Providers Memoh can create from models.dev, maintained by hand. Each entry
// names the models.dev provider it comes from (source), how it is shown, and the
// client type and base URL a provider created from it uses. The model list and
// logo come from models.dev at runtime; icon, when set, is a local icon that
// replaces the models.dev logo.
//
// Add a provider only after checking that its endpoint speaks the client type
// listed here. Providers that mix protocols, need a base URL with variables, or
// did not answer like an API when checked are left out until someone verifies
// them.
export type ModelSourceClientType =
  | 'openai-completions'
  | 'openai-responses'
  | 'anthropic-messages'
  | 'google-generative-ai'

export interface ModelSource {
  // models.dev provider id.
  source: string
  name: string
  client_type: ModelSourceClientType
  base_url: string
  icon?: string
  // The models.dev logo carries its own colors; others are drawn in one color
  // and take the text color.
  colored_logo?: boolean
}

export const MODEL_SOURCES: readonly ModelSource[] = [
  { source: '302ai', name: '302.AI', client_type: 'openai-completions', base_url: 'https://api.302.ai/v1', colored_logo: true },
  { source: 'abacus', name: 'Abacus', client_type: 'openai-completions', base_url: 'https://routellm.abacus.ai/v1' },
  { source: 'abliteration-ai', name: 'abliteration.ai', client_type: 'openai-completions', base_url: 'https://api.abliteration.ai/v1' },
  { source: 'above', name: 'above.dev', client_type: 'openai-completions', base_url: 'https://api.above.dev/v1' },
  { source: 'agnes', name: 'Agnes AI', client_type: 'openai-completions', base_url: 'https://apihub.agnes-ai.com/v1' },
  { source: 'ai-router', name: 'AI-ROUTER', client_type: 'openai-completions', base_url: 'https://api.ai-router.dev/v1' },
  { source: 'ai21', name: 'AI21 Labs', client_type: 'openai-completions', base_url: 'https://api.ai21.com/studio/v1' },
  { source: 'aiand', name: 'ai&', client_type: 'openai-completions', base_url: 'https://api.aiand.com/v1' },
  { source: 'aihubmix', name: 'AIHubMix', client_type: 'openai-completions', base_url: 'https://aihubmix.com/v1' },
  { source: 'aimlapi', name: 'AI/ML API', client_type: 'openai-completions', base_url: 'https://api.aimlapi.com/v1' },
  { source: 'ainetcafe', name: 'ainetcafe', client_type: 'openai-completions', base_url: 'https://microquickjs.com/v1' },
  { source: 'aixy', name: 'Aixy', client_type: 'openai-responses', base_url: 'https://api.aixy-gateway.com/v1' },
  { source: 'aki-io', name: 'AKI.IO', client_type: 'openai-completions', base_url: 'https://aki.io/v1' },
  { source: 'alibaba', name: 'Alibaba', client_type: 'openai-completions', base_url: 'https://dashscope-intl.aliyuncs.com/compatible-mode/v1' },
  { source: 'alibaba-cn', name: 'Aliyun Bailian', client_type: 'openai-completions', base_url: 'https://dashscope.aliyuncs.com/compatible-mode/v1', icon: 'qwen-color' },
  { source: 'alibaba-coding-plan', name: 'Alibaba Coding Plan', client_type: 'openai-completions', base_url: 'https://coding-intl.dashscope.aliyuncs.com/v1' },
  { source: 'alibaba-coding-plan-cn', name: 'Alibaba Coding Plan (China)', client_type: 'openai-completions', base_url: 'https://coding.dashscope.aliyuncs.com/v1' },
  { source: 'alibaba-token-plan', name: 'Alibaba Token Plan', client_type: 'openai-completions', base_url: 'https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1' },
  { source: 'alibaba-token-plan-cn', name: 'Alibaba Token Plan (China)', client_type: 'openai-completions', base_url: 'https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1' },
  { source: 'ambient', name: 'Ambient', client_type: 'openai-completions', base_url: 'https://api.ambient.xyz/v1' },
  { source: 'amd', name: 'AMD', client_type: 'openai-completions', base_url: 'https://developer.amd.com.cn/radeon/api/v1' },
  { source: 'anthropic', name: 'Anthropic', client_type: 'anthropic-messages', base_url: 'https://api.anthropic.com/v1', icon: 'anthropic' },
  { source: 'anyapi', name: 'AnyAPI', client_type: 'openai-completions', base_url: 'https://api.anyapi.ai/v1' },
  { source: 'arcee', name: 'Arcee', client_type: 'openai-completions', base_url: 'https://api.arcee.ai/api/v1' },
  { source: 'auriko', name: 'Auriko', client_type: 'openai-completions', base_url: 'https://api.auriko.ai/v1' },
  { source: 'bailing', name: 'Bailing', client_type: 'openai-completions', base_url: 'https://api.tbox.cn/api/llm/v1' },
  { source: 'baseten', name: 'Baseten', client_type: 'openai-completions', base_url: 'https://inference.baseten.co/v1' },
  { source: 'bee', name: 'Bee by HEOSSI', client_type: 'openai-completions', base_url: 'https://api.bee.heossi.com/bee' },
  { source: 'berget', name: 'Berget.AI', client_type: 'openai-completions', base_url: 'https://api.berget.ai/v1' },
  { source: 'blueclaw', name: 'Blue Claw', client_type: 'openai-completions', base_url: 'https://openai.blueclaw.network/v1' },
  { source: 'bothub', name: 'Bothub', client_type: 'openai-completions', base_url: 'https://openai.bothub.ru/v1' },
  { source: 'cerebras', name: 'Cerebras', client_type: 'openai-completions', base_url: 'https://api.cerebras.ai/v1', icon: 'cerebras-color' },
  { source: 'claudinio', name: 'Claudinio', client_type: 'openai-completions', base_url: 'https://api.claudin.io/v1' },
  { source: 'cline-pass', name: 'ClinePass', client_type: 'openai-completions', base_url: 'https://api.cline.bot/api/v1' },
  { source: 'coralbricks', name: 'CoralBricks', client_type: 'openai-completions', base_url: 'https://inference.coralbricks.ai/v1' },
  { source: 'cortecs', name: 'Cortecs', client_type: 'openai-completions', base_url: 'https://api.cortecs.ai/v1' },
  { source: 'crossmodel', name: 'CrossModel', client_type: 'openai-completions', base_url: 'https://api.crossmodel.ai/v1' },
  { source: 'crusoe', name: 'Crusoe', client_type: 'openai-completions', base_url: 'https://api.inference.crusoecloud.com/v1' },
  { source: 'daoxe', name: 'DaoXE', client_type: 'openai-completions', base_url: 'https://daoxe.com/v1' },
  { source: 'deepinfra', name: 'Deep Infra', client_type: 'openai-completions', base_url: 'https://api.deepinfra.com/v1/openai' },
  { source: 'deepseek', name: 'DeepSeek', client_type: 'openai-completions', base_url: 'https://api.deepseek.com', icon: 'deepseek-color' },
  { source: 'digitalocean', name: 'DigitalOcean', client_type: 'openai-completions', base_url: 'https://inference.do-ai.run/v1' },
  { source: 'dinference', name: 'DInference', client_type: 'openai-completions', base_url: 'https://api.dinference.com/v1' },
  { source: 'drun', name: 'D.Run (China)', client_type: 'openai-completions', base_url: 'https://chat.d.run/v1' },
  { source: 'ebcloud', name: 'EBCloud', client_type: 'openai-completions', base_url: 'https://maas-api.ebcloud.com/v1' },
  { source: 'echo', name: 'Echo', client_type: 'openai-completions', base_url: 'https://echo.tracerml.ai/v1' },
  { source: 'edenai', name: 'Eden AI', client_type: 'openai-completions', base_url: 'https://api.edenai.run/v3' },
  { source: 'empiriolabs', name: 'EmpirioLabs AI', client_type: 'openai-completions', base_url: 'https://api.empiriolabs.ai/v1' },
  { source: 'engy', name: 'engy', client_type: 'openai-completions', base_url: 'https://api.engy.ai/v1' },
  { source: 'evroc', name: 'evroc', client_type: 'openai-completions', base_url: 'https://models.think.evroc.com/v1' },
  { source: 'fastrouter', name: 'FastRouter', client_type: 'openai-completions', base_url: 'https://go.fastrouter.ai/api/v1' },
  { source: 'fireworks-ai', name: 'Fireworks AI', client_type: 'openai-completions', base_url: 'https://api.fireworks.ai/inference/v1', icon: 'fireworks-color' },
  { source: 'friendli', name: 'Friendli', client_type: 'openai-completions', base_url: 'https://api.friendli.ai/serverless/v1' },
  { source: 'frogbot', name: 'FrogBot', client_type: 'openai-completions', base_url: 'https://app.frogbot.ai/api/v1', colored_logo: true },
  { source: 'gitlab', name: 'GitLab Duo', client_type: 'openai-completions', base_url: 'https://gitlab.com/api/v4' },
  { source: 'google', name: 'Google', client_type: 'google-generative-ai', base_url: 'https://generativelanguage.googleapis.com/v1beta', icon: 'google-color' },
  { source: 'groq', name: 'Groq', client_type: 'openai-completions', base_url: 'https://api.groq.com/openai/v1', icon: 'groq' },
  { source: 'hpc-ai', name: 'HPC-AI', client_type: 'openai-completions', base_url: 'https://api.hpc-ai.com/inference/v1', colored_logo: true },
  { source: 'huggingface', name: 'HuggingFace', client_type: 'openai-completions', base_url: 'https://router.huggingface.co/v1', icon: 'huggingface-color' },
  { source: 'hyper', name: 'Charm Hyper', client_type: 'openai-completions', base_url: 'https://hyper.charm.land/v1' },
  { source: 'iflowcn', name: 'iFlow', client_type: 'openai-completions', base_url: 'https://apis.iflow.cn/v1' },
  { source: 'impossibl', name: 'Impossibl', client_type: 'openai-completions', base_url: 'https://api.impossibl.com/v1' },
  { source: 'inception', name: 'Inception', client_type: 'openai-completions', base_url: 'https://api.inceptionlabs.ai/v1' },
  { source: 'inceptron', name: 'Inceptron', client_type: 'openai-completions', base_url: 'https://api.inceptron.io/v1' },
  { source: 'inco', name: 'Inco', client_type: 'openai-completions', base_url: 'https://api.inco.ai/v1' },
  { source: 'infer', name: 'Infer by Flow7', client_type: 'openai-responses', base_url: 'https://infer.flow7.org/v1' },
  { source: 'io-net', name: 'IO.NET', client_type: 'openai-completions', base_url: 'https://api.intelligence.io.solutions/api/v1' },
  { source: 'iteracompute', name: 'IteraCompute', client_type: 'openai-completions', base_url: 'https://api.iteracompute.com/v1' },
  { source: 'jalapeno', name: 'Jalapeno Cloud', client_type: 'openai-completions', base_url: 'https://api.jalapeno-cloud.ai/v1' },
  { source: 'jiekou', name: 'Jiekou.AI', client_type: 'openai-completions', base_url: 'https://api.jiekou.ai/openai' },
  { source: 'kimi-code-plan-cn', name: 'Kimi For Coding (kimi.com)', client_type: 'openai-completions', base_url: 'https://api.kimi.com/coding/v1' },
  { source: 'kimi-code-plan-global', name: 'Kimi For Coding (kimi.ai)', client_type: 'openai-completions', base_url: 'https://api.kimi.ai/coding/v1' },
  { source: 'klokintegration', name: 'klokintegration.se', client_type: 'openai-completions', base_url: 'https://api-gw.klok.ipaas.se/proxy/kloker-key/v1' },
  { source: 'kosmik', name: 'Kosmik Compute', client_type: 'openai-completions', base_url: 'https://api.koscompute.com/v1' },
  { source: 'kuae-cloud-coding-plan', name: 'KUAE Cloud Coding Plan', client_type: 'openai-completions', base_url: 'https://coding-plan-endpoint.kuaecloud.net/v1' },
  { source: 'llama', name: 'Llama', client_type: 'openai-completions', base_url: 'https://api.llama.com/compat/v1' },
  { source: 'llmgateway', name: 'DevPass (LLM Gateway)', client_type: 'openai-completions', base_url: 'https://api.llmgateway.io/v1' },
  { source: 'llmgateway-providers', name: 'LLM Gateway', client_type: 'openai-completions', base_url: 'https://api.llmgateway.io/v1' },
  { source: 'llmtech', name: 'LLM Tech', client_type: 'openai-completions', base_url: 'https://api.llmtech.eu/v1' },
  { source: 'llmtr', name: 'LLMTR', client_type: 'openai-completions', base_url: 'https://llmtr.com/v1' },
  { source: 'lmstudio', name: 'LM Studio', client_type: 'openai-completions', base_url: 'http://127.0.0.1:1234/v1', icon: 'lmstudio' },
  { source: 'longcat', name: 'LongCat', client_type: 'openai-completions', base_url: 'https://api.longcat.chat/openai' },
  { source: 'lowrouter', name: 'LowRouter', client_type: 'openai-completions', base_url: 'https://api.lowrouter.ai/v1' },
  { source: 'lucidquery', name: 'LucidQuery', client_type: 'openai-completions', base_url: 'https://api.lucidquery.com/v1' },
  { source: 'meganova', name: 'Meganova', client_type: 'openai-completions', base_url: 'https://api.meganova.ai/v1' },
  { source: 'melious', name: 'Melious', client_type: 'openai-completions', base_url: 'https://api.melious.ai/v1' },
  { source: 'merge-gateway', name: 'Merge Gateway', client_type: 'openai-completions', base_url: 'https://api-gateway.merge.dev/v1/ai-sdk' },
  { source: 'meta', name: 'Meta', client_type: 'openai-responses', base_url: 'https://api.meta.ai/v1' },
  { source: 'minimax-cn', name: 'MiniMax (minimax.cn)', client_type: 'anthropic-messages', base_url: 'https://api.minimax.cn/anthropic/v1' },
  { source: 'minimax-cn-coding-plan', name: 'MiniMax Token Plan (minimax.cn)', client_type: 'anthropic-messages', base_url: 'https://api.minimax.cn/anthropic/v1' },
  { source: 'minimax-coding-plan', name: 'MiniMax Token Plan (minimax.io)', client_type: 'anthropic-messages', base_url: 'https://api.minimax.io/anthropic/v1' },
  { source: 'mistral', name: 'Mistral', client_type: 'openai-completions', base_url: 'https://api.mistral.ai/v1', icon: 'mistral-color' },
  { source: 'mixlayer', name: 'Mixlayer', client_type: 'openai-completions', base_url: 'https://models.mixlayer.ai/v1' },
  { source: 'moark', name: 'Moark', client_type: 'openai-completions', base_url: 'https://moark.com/v1' },
  { source: 'modal', name: 'Modal', client_type: 'openai-completions', base_url: 'https://inference.us-west.modal.direct/v1' },
  { source: 'model-oracle-ai', name: 'Model Oracle AI', client_type: 'openai-completions', base_url: 'https://api.modeloracle.com/api/v1' },
  { source: 'modelis', name: 'Modelis', client_type: 'openai-completions', base_url: 'https://modelishub.com/v1' },
  { source: 'modelscope', name: 'ModelScope', client_type: 'openai-completions', base_url: 'https://api-inference.modelscope.cn/v1' },
  { source: 'moonshotai', name: 'Moonshot AI', client_type: 'openai-completions', base_url: 'https://api.moonshot.ai/v1' },
  { source: 'moonshotai-cn', name: 'Moonshot', client_type: 'openai-completions', base_url: 'https://api.moonshot.cn/v1', icon: 'moonshot' },
  { source: 'morph', name: 'Morph', client_type: 'openai-completions', base_url: 'https://api.morphllm.com/v1' },
  { source: 'nan', name: 'NaN', client_type: 'openai-completions', base_url: 'https://api.nan.builders/v1' },
  { source: 'nano-gpt', name: 'NanoGPT', client_type: 'openai-completions', base_url: 'https://nano-gpt.com/api/v1' },
  { source: 'nearai', name: 'NEAR AI Cloud', client_type: 'openai-completions', base_url: 'https://cloud-api.near.ai/v1' },
  { source: 'nebius', name: 'Nebius Token Factory', client_type: 'openai-completions', base_url: 'https://api.tokenfactory.nebius.com/v1' },
  { source: 'neuralwatt', name: 'Neuralwatt', client_type: 'openai-completions', base_url: 'https://api.neuralwatt.com/v1' },
  { source: 'novita-ai', name: 'Novita AI', client_type: 'openai-completions', base_url: 'https://api.novita.ai/openai', colored_logo: true },
  { source: 'nvidia', name: 'Nvidia', client_type: 'openai-completions', base_url: 'https://integrate.api.nvidia.com/v1' },
  { source: 'oci', name: 'OCI Generative AI', client_type: 'openai-completions', base_url: 'https://inference.generativeai.us-chicago-1.oci.oraclecloud.com/openai/v1' },
  { source: 'ollama-cloud', name: 'Ollama Cloud', client_type: 'openai-completions', base_url: 'https://ollama.com/v1' },
  { source: 'openai', name: 'OpenAI', client_type: 'openai-responses', base_url: 'https://api.openai.com/v1', icon: 'openai' },
  { source: 'openreason', name: 'OpenReason', client_type: 'openai-completions', base_url: 'https://api.openreason.app/v1' },
  { source: 'openrouter', name: 'OpenRouter', client_type: 'openai-completions', base_url: 'https://openrouter.ai/api/v1', icon: 'openrouter' },
  { source: 'opper', name: 'Opper', client_type: 'openai-completions', base_url: 'https://api.opper.ai/v3/compat' },
  { source: 'orcarouter', name: 'OrcaRouter', client_type: 'openai-completions', base_url: 'https://api.orcarouter.ai/v1', colored_logo: true },
  { source: 'pareto', name: 'Pareto Inference', client_type: 'openai-completions', base_url: 'https://api.paretoinference.com/v1' },
  { source: 'pendra', name: 'Pendra', client_type: 'openai-completions', base_url: 'https://api.pendra.ai/api/v1' },
  { source: 'perplexity', name: 'Perplexity', client_type: 'openai-completions', base_url: 'https://api.perplexity.ai', icon: 'perplexity-color' },
  { source: 'perplexity-agent', name: 'Perplexity Agent', client_type: 'openai-responses', base_url: 'https://api.perplexity.ai/v1' },
  { source: 'pioneer', name: 'Pioneer', client_type: 'openai-completions', base_url: 'https://api.fastino.ai/v1' },
  { source: 'poe', name: 'Poe', client_type: 'openai-completions', base_url: 'https://api.poe.com/v1' },
  { source: 'poolside', name: 'Poolside', client_type: 'openai-completions', base_url: 'https://inference.poolside.ai/v1' },
  { source: 'prime', name: 'Prime Inference', client_type: 'openai-completions', base_url: 'https://api.pinference.ai/api/v1' },
  { source: 'qihang-ai', name: 'QiHang', client_type: 'openai-completions', base_url: 'https://api.qhaigc.net/v1' },
  { source: 'qiniu-ai', name: 'Qiniu', client_type: 'openai-completions', base_url: 'https://api.qnaigc.com/v1' },
  { source: 'regolo-ai', name: 'Regolo AI', client_type: 'openai-completions', base_url: 'https://api.regolo.ai/v1', colored_logo: true },
  { source: 'requesty', name: 'Requesty', client_type: 'openai-completions', base_url: 'https://router.requesty.ai/v1' },
  { source: 'routing-run', name: 'routing.run', client_type: 'openai-completions', base_url: 'https://api.routing.run/v1' },
  { source: 'runinfra', name: 'RunInfra', client_type: 'openai-completions', base_url: 'https://api.runinfra.ai/v1' },
  { source: 'sarvam', name: 'Sarvam AI', client_type: 'openai-completions', base_url: 'https://api.sarvam.ai/v1' },
  { source: 'scaleway', name: 'Scaleway', client_type: 'openai-completions', base_url: 'https://api.scaleway.ai/v1' },
  { source: 'scnet-token-plan', name: 'SCNet Token Plan', client_type: 'openai-completions', base_url: 'https://api.scnet.cn/api/llm/v1' },
  { source: 'scx-ai', name: 'SCX.ai', client_type: 'openai-completions', base_url: 'https://api.scx.ai/v1' },
  { source: 'sensenova', name: 'SenseNova (China)', client_type: 'openai-completions', base_url: 'https://token.sensenova.cn/v1' },
  { source: 'siliconflow', name: 'SiliconFlow', client_type: 'openai-completions', base_url: 'https://api.siliconflow.com/v1' },
  { source: 'siliconflow-cn', name: 'SiliconFlow (China)', client_type: 'openai-completions', base_url: 'https://api.siliconflow.cn/v1' },
  { source: 'standardcompute', name: 'Standard Compute', client_type: 'openai-completions', base_url: 'https://api.stdcmpt.com/v1' },
  { source: 'stepfun', name: 'StepFun (China)', client_type: 'openai-completions', base_url: 'https://api.stepfun.com/v1' },
  { source: 'stepfun-ai', name: 'StepFun (Global)', client_type: 'openai-completions', base_url: 'https://api.stepfun.ai/v1' },
  { source: 'stepfun-ai-step-plan', name: 'StepFun Step Plan (Global)', client_type: 'openai-completions', base_url: 'https://api.stepfun.ai/step_plan/v1' },
  { source: 'stepfun-step-plan', name: 'StepFun Step Plan (China)', client_type: 'openai-completions', base_url: 'https://api.stepfun.com/step_plan/v1' },
  { source: 'subconscious', name: 'Subconscious', client_type: 'anthropic-messages', base_url: 'https://api.subconscious.dev/v1' },
  { source: 'submodel', name: 'submodel', client_type: 'openai-completions', base_url: 'https://llm.submodel.ai/v1' },
  { source: 'synthetic', name: 'Synthetic', client_type: 'openai-completions', base_url: 'https://api.synthetic.new/openai/v1' },
  { source: 'tempr', name: 'Tempr Gateway', client_type: 'openai-completions', base_url: 'https://api.temprhq.io/v1' },
  { source: 'tencent-coding-plan', name: 'Tencent Coding Plan (China)', client_type: 'openai-completions', base_url: 'https://api.lkeap.cloud.tencent.com/coding/v3' },
  { source: 'tencent-token-plan', name: 'Tencent Token Plan', client_type: 'openai-completions', base_url: 'https://api.lkeap.cloud.tencent.com/plan/v3' },
  { source: 'tencent-tokenhub', name: 'Tencent TokenHub', client_type: 'openai-completions', base_url: 'https://tokenhub.tencentmaas.com/v1' },
  { source: 'tensorx', name: 'TensorX', client_type: 'openai-completions', base_url: 'https://api.tensorx.ai/v1' },
  { source: 'the-grid-ai', name: 'The Grid AI', client_type: 'openai-completions', base_url: 'https://api.thegrid.ai/v1' },
  { source: 'thinkingmachines', name: 'Thinking Machines', client_type: 'anthropic-messages', base_url: 'https://tinker.thinkingmachines.dev/services/tinker-prod/anthropic/api/v1' },
  { source: 'tinfoil', name: 'Tinfoil', client_type: 'openai-completions', base_url: 'https://inference.tinfoil.sh/v1' },
  { source: 'togetherai', name: 'Together AI', client_type: 'openai-completions', base_url: 'https://api.together.xyz/v1', icon: 'together-color' },
  { source: 'tokengo', name: 'TokenGo', client_type: 'openai-completions', base_url: 'https://api.tokengo.com/v1' },
  { source: 'tokenrouter', name: 'TokenRouter', client_type: 'openai-completions', base_url: 'https://api.tokenrouter.com/v1' },
  { source: 'trustedrouter', name: 'TrustedRouter', client_type: 'openai-completions', base_url: 'https://api.trustedrouter.com/v1' },
  { source: 'umans-ai', name: 'Umans AI', client_type: 'openai-completions', base_url: 'https://api.code.umans.ai/v1', colored_logo: true },
  { source: 'umans-ai-coding-plan', name: 'Umans AI Coding Plan', client_type: 'openai-completions', base_url: 'https://api.code.umans.ai/v1', colored_logo: true },
  { source: 'unorouter', name: 'UnoRouter', client_type: 'openai-completions', base_url: 'https://api.unorouter.com/v1' },
  { source: 'upstage', name: 'Upstage', client_type: 'openai-completions', base_url: 'https://api.upstage.ai/v1/solar' },
  { source: 'vancine', name: 'Vancine', client_type: 'openai-completions', base_url: 'https://vancine.com/v1' },
  { source: 'venice', name: 'Venice AI', client_type: 'openai-completions', base_url: 'https://api.venice.ai/api/v1' },
  { source: 'vispark', name: 'Vispark', client_type: 'openai-completions', base_url: 'https://api.lab.vispark.in/v1' },
  { source: 'volcengine', name: 'Volcengine Ark', client_type: 'openai-completions', base_url: 'https://ark.cn-beijing.volces.com/api/v3' },
  { source: 'volcengine-coding-plan', name: 'Volcengine Ark Coding Plan', client_type: 'openai-completions', base_url: 'https://ark.cn-beijing.volces.com/api/coding/v3' },
  { source: 'vultr', name: 'Vultr', client_type: 'openai-completions', base_url: 'https://api.vultrinference.com/v1' },
  { source: 'wafer.ai', name: 'Wafer', client_type: 'openai-completions', base_url: 'https://pass.wafer.ai/v1', colored_logo: true },
  { source: 'wallaby', name: 'Wallaby', client_type: 'openai-completions', base_url: 'https://api.wallabytoken.com/v1' },
  { source: 'wandb', name: 'CoreWeave', client_type: 'openai-completions', base_url: 'https://api.inference.wandb.ai/v1' },
  { source: 'xiaomi', name: 'Xiaomi', client_type: 'openai-completions', base_url: 'https://api.xiaomimimo.com/v1' },
  { source: 'xiaomi-token-plan-ams', name: 'Xiaomi Token Plan (Europe)', client_type: 'openai-completions', base_url: 'https://token-plan-ams.xiaomimimo.com/v1' },
  { source: 'xiaomi-token-plan-cn', name: 'Xiaomi Token Plan (China)', client_type: 'openai-completions', base_url: 'https://token-plan-cn.xiaomimimo.com/v1' },
  { source: 'xiaomi-token-plan-sgp', name: 'Xiaomi Token Plan (Singapore)', client_type: 'openai-completions', base_url: 'https://token-plan-sgp.xiaomimimo.com/v1' },
  { source: 'xpersona', name: 'Xpersona', client_type: 'openai-completions', base_url: 'https://www.xpersona.co/v1' },
  { source: 'zai', name: 'Z.AI GLM', client_type: 'openai-completions', base_url: 'https://api.z.ai/api/paas/v4', icon: 'zhipu-color' },
  { source: 'zeldoc', name: 'Zeldoc', client_type: 'openai-completions', base_url: 'https://api.zeldoc.ai/v1', colored_logo: true },
  { source: 'zhipuai', name: 'Zhipu AI', client_type: 'openai-completions', base_url: 'https://open.bigmodel.cn/api/paas/v4', icon: 'zhipu-color' },
  { source: 'zhipuai-coding-plan', name: 'Zhipu AI Coding Plan', client_type: 'openai-completions', base_url: 'https://open.bigmodel.cn/api/coding/paas/v4' },
]

const bySource = new Map(MODEL_SOURCES.map(item => [item.source, item]))

export function findModelSource(source: string | null | undefined): ModelSource | undefined {
  return source ? bySource.get(source) : undefined
}

// The logo models.dev serves for a provider; it answers with a generic logo for
// an id it does not know.
export function modelSourceLogo(item: ModelSource): string {
  return item.icon ?? `https://models.dev/logos/${encodeURIComponent(item.source)}.svg`
}

// Former provider templates an entry above replaces, by template key. Those
// templates are no longer offered; a provider created from one still counts as
// configured for its source.
export const TEMPLATE_SOURCES: Readonly<Record<string, string>> = {
  'anthropic': 'anthropic',
  'cerebras': 'cerebras',
  'deepseek': 'deepseek',
  'fireworks': 'fireworks-ai',
  'google': 'google',
  'groq': 'groq',
  'huggingface': 'huggingface',
  'lmstudio': 'lmstudio',
  'mistral': 'mistral',
  'moonshot': 'moonshotai-cn',
  'openai': 'openai',
  'openrouter': 'openrouter',
  'perplexity': 'perplexity',
  'qwen': 'alibaba-cn',
  'together': 'togetherai',
  'zai': 'zai',
  'zhipu': 'zhipuai',
}

// A local endpoint usually runs without an API key.
export function modelSourceNeedsApiKey(item: ModelSource): boolean {
  const host = new URL(item.base_url).hostname
  return host !== 'localhost' && host !== '127.0.0.1'
}

const MODELS_DEV_LOGO = /^https:\/\/models\.dev\/logos\/([^/]+)\.svg$/

// Whether an icon is a models.dev logo drawn in one color, which should take the
// text color rather than render as an image.
export function isMonochromeLogo(icon: string | null | undefined): boolean {
  const match = icon ? MODELS_DEV_LOGO.exec(icon) : null
  if (!match) return false
  return !findModelSource(decodeURIComponent(match[1]!))?.colored_logo
}
