import type { Request } from "./transport";
export type BookingId = string | number;
export interface Booking { id: BookingId; 名称: string; }
export namespace Reservations {
  @sealed
  export class Scheduler<T extends Booking> {
    @trace
    async reserve(request: Request<T>): Promise<T> {
      return request.body;
    }
  }
  export const cancel = (id: BookingId): boolean => !!id;
}
export { Scheduler as BookingScheduler } from "./scheduler";
