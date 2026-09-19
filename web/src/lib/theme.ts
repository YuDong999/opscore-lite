// 主题工具(L2 公共层): canvas / EChart 等"不接受 var()"的运行时场景用。
// 在组件 render/构建 option 时调用即得当前主题具体值; 主题切换会触发重渲染, 值随之刷新。
// CSS/内联样式里请直接写主题变量(如 var(--danger)), 不要用本函数。
export const cssVar = (name: string): string =>
  getComputedStyle(document.documentElement).getPropertyValue(name).trim()
