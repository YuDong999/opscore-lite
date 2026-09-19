// 虚拟模块类型声明(实现在 vite.module.config.ts 的 standaloneModulePlugin 构建期注入)
declare module 'virtual:standalone-module' {
  const loader: () => Promise<{ default: React.ComponentType<any> }>
  export default loader
}
