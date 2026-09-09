export interface Config {
  log: LogConfig;
  server: ServerConfig;
  admin: AdminConfig;
  auth: AuthConfig;
  database: DatabaseConfig;
  container: ContainerConfig;
  containerd: ContainerdConfig;
  docker?: DockerConfig;
  apple?: AppleConfig;
  workspace?: WorkspaceConfig;
  postgres: PostgresConfig;
  pgvector?: PGVectorConfig;
  agent_gateway: AgentGatewayConfig;
  supermarket: SupermarketConfig;
  web: WebConfig;
}

export interface LogConfig {
  level: string;
  format: string;
}

export interface ServerConfig {
  addr: string;
}

export interface AdminConfig {
  username: string;
  password: string;
  email: string;
}

export interface AuthConfig {
  jwt_secret: string;
  jwt_expires_in: string;
}

export interface DatabaseConfig {
  driver: string;
}

export interface ContainerConfig extends WorkspaceConfig {
  backend: string;
}

export interface ContainerdConfig {
  socket_path: string;
  namespace: string;
}

export interface DockerConfig {
  host?: string;
  network?: string;
  server_container?: string;
}

export interface AppleConfig {
  socket_path?: string;
  binary_path?: string;
}

export interface WorkspaceConfig {
  registry?: string;
  default_image: string;
  image_pull_policy?: string;
  snapshotter: string;
  data_root: string;
  cni_bin_dir?: string;
  cni_conf_dir?: string;
  bridge_path?: string;
  /** @deprecated Use bridge_path. */
  runtime_dir?: string;
}

export interface PostgresConfig {
  host: string;
  port: number;
  user: string;
  password: string;
  database: string;
  sslmode: string;
}

export interface PGVectorConfig extends PostgresConfig {
  enabled: boolean;
}

export interface AgentGatewayConfig {
  host: string;
  port: number;
  server_addr?: string;
}

export interface SupermarketConfig {
  base_url?: string;
}

export interface WebConfig {
  host: string;
  port: number;
}
