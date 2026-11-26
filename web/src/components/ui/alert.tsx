import * as React from "react";
import { cn } from "@/lib/utils";

export const Alert = React.forwardRef<HTMLDivElement, React.HTMLAttributes<HTMLDivElement>>(({ className, ...props }, ref) => <div ref={ref} role="alert" className={cn("relative grid w-full grid-cols-[auto_1fr] gap-x-3 gap-y-1 rounded-lg border p-4 text-sm [&>svg]:mt-0.5", className)} {...props} />);
Alert.displayName = "Alert";
export const AlertTitle = React.forwardRef<HTMLHeadingElement, React.HTMLAttributes<HTMLHeadingElement>>(({ className, ...props }, ref) => <h5 ref={ref} className={cn("col-start-2 font-medium leading-none", className)} {...props} />);
AlertTitle.displayName = "AlertTitle";
export const AlertDescription = React.forwardRef<HTMLParagraphElement, React.HTMLAttributes<HTMLParagraphElement>>(({ className, ...props }, ref) => <div ref={ref} className={cn("col-start-2 text-sm text-muted-foreground [&_p]:leading-relaxed", className)} {...props} />);
AlertDescription.displayName = "AlertDescription";
