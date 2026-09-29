import { send } from "./api.js";
// 创建预约😀
export async function createBooking(名称) {
  return await send({ 名称 });
}
export const cancelBooking = id => send({ id });
