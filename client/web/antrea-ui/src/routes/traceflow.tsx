import React, { useState, useEffect, useRef} from 'react';
import { useNavigate, Outlet } from "react-router-dom";
import { useForm, SubmitHandler } from "react-hook-form";
import { CdsAlertGroup, CdsAlert } from "@cds/react/alert";
import { CdsButton } from '@cds/react/button';
import { CdsFormGroup } from '@cds/react/forms';
import { CdsInput } from "@cds/react/input";
import { CdsRadioGroup, CdsRadio } from '@cds/react/radio';
import { CdsSelect } from '@cds/react/select';
import { ErrorMessage } from '@hookform/error-message';
import { ErrorMessageContainer } from '../components/form-errors';
import {isIP, isIPv4} from 'is-ip';
import {TraceflowPacket, TraceflowSpec, traceflowAPI} from '../api/traceflow';
import { APIError } from '../api/common';
import { useAPIError} from '../components/errors';

type Inputs = {
    srcNamespace: string
    srcPod: string
    srcPort: number
    destinationType: string
    dstNamespace: string
    dst: string
    dstPort: number
    protocol: string
    timeout: number
};

function createTraceflowRequest(inputs: Inputs): TraceflowSpec {
    const packet: TraceflowPacket = {
        ipHeader: {},
        transportHeader: {},
    };
    switch (inputs.protocol) {
            case "ICMP": {
                packet.ipHeader.protocol = 1;
                packet.transportHeader.icmp = {};
                break;
            }
            case "TCP": {
                packet.ipHeader.protocol = 6;
                packet.transportHeader.tcp = {
                    srcPort: inputs.srcPort,
                    dstPort: inputs.dstPort,
                    flags: 2,
                };
                break;
            }
            case "UDP": {
                packet.ipHeader.protocol = 17;
                packet.transportHeader.udp = {
                    srcPort: inputs.srcPort,
                    dstPort: inputs.dstPort,
                };
                break;
            }
    }
    
    const spec: TraceflowSpec = {
        source: {},
        destination: {},
    };
    if (isIP(inputs.srcPod)) {
        spec.source.ip = inputs.srcPod;
    } else {
        spec.source.namespace = inputs.srcNamespace;
        spec.source.pod = inputs.srcPod;
    }
    switch (inputs.destinationType) {
            case "Pod": {
                spec.destination.namespace = inputs.dstNamespace;
                spec.destination.pod = inputs.dst;
                break;
            }
            case "Service": {
                spec.destination.namespace = inputs.dstNamespace;
                spec.destination.service = inputs.dst;
                break;
            }
            case "IPv4": {
                if (!isIPv4(inputs.dst)) {
                    throw new Error("Invalid destination IP address");
                }
                spec.destination.ip = inputs.dst;
            }
    }
    return spec;
}

function TraceflowRunningAlert(props: { traceflowRunning: boolean }) {
    const traceflowRunning = props.traceflowRunning;

    if (!traceflowRunning) return null;

    return (
        <CdsAlertGroup type="banner" status="info">
            <CdsAlert status="loading">Running Traceflow, it could take a few seconds</CdsAlert>
        </CdsAlertGroup>
    );
}

export default function Traceflow() {
    const { register, handleSubmit, reset, formState: { errors } } = useForm<Inputs>({
        defaultValues: {
            srcPort: 32678,
            destinationType: "Pod",
            dstPort: 80,
            protocol: "TCP",
            timeout: 20,
        }
    });

    const navigate = useNavigate();

    const [traceflowRunning, setTraceflowRunning] = useState<boolean>(false);
    const mountedRef = useRef<boolean>(false);

    const { addError } = useAPIError();

    async function runTraceflow(tf: TraceflowSpec, cb: () => void) {
        try {
            const tfStatus = await traceflowAPI.runTraceflow(tf, true);
            if (tfStatus === undefined) {
                throw new Error("missing Traceflow status");
            }
            navigate(`/traceflow/result`, {
                state: {
                    spec: tf,
                    status: tfStatus,
                }
            });
        } catch(e) {
            if (e instanceof APIError) addError(e);
            else throw e;
        }
        if (mountedRef.current) {
            cb();
        }
    }

    const onSubmit: SubmitHandler<Inputs> = data => {
        const tf = createTraceflowRequest(data);
        setTraceflowRunning(true);
        runTraceflow(tf, () => {
            setTraceflowRunning(false);
        });
    };

    useEffect(() => {
        mountedRef.current = true;
        return () => {
            mountedRef.current = false;
        };
    }, []);

    const srcPort = register(
        "srcPort",
        {
            min: {
                value: 1,
                message: "source port must be >= 1",
            },
            max: {
                value: 65535,
                message: "source port must be <= 65535",
            },
        }
    );

    const destinationType = register(
        "destinationType",
        {
            required: true,
        }
    );

    const dstPort = register(
        "dstPort",
        {
            min: {
                value: 1,
                message: "destination port must be >= 1",
            },
            max: {
                value: 65535,
                message: "destination port must be <= 65535",
            },
        }
    );

    const timeout = register(
        "timeout",
        {
            min: {
                value: 1,
                message: "timeout must be >= 1",
            },
            max: {
                value: 65535,
                message: "timeout must be <= 120",
            },
        },
    );

    return (
        <main>
            <div cds-layout="horizontal gap:lg">
            <div cds-layout="vertical gap:lg">
                <p cds-text="title">Traceflow</p>
                <form onSubmit = {handleSubmit(onSubmit)}>
                    <CdsFormGroup layout="horizontal">
                        <CdsInput>
                            <label>Source Namespace</label>
                            <input {...register("srcNamespace")} defaultValue="default" />
                        </CdsInput>
                        <CdsInput>
                            <label>Source Pod</label>
                            <input {...register("srcPod", { required: "Source Pod is required" })} placeholder="Name or IP" />
                        </CdsInput>
                        <ErrorMessage
                            errors={errors}
                            name="srcPod"
                            as={<ErrorMessageContainer />}
                        />
                        <CdsInput>
                            <label>Source Port</label>
                            <input type="number" {...srcPort} />
                        </CdsInput>
                        <ErrorMessage
                            errors={errors}
                            name={srcPort.name}
                            as={<ErrorMessageContainer />}
                        />
                        <CdsRadioGroup>
                            <label>Destination Type</label>
                            <CdsRadio key="pod">
                                <label>Pod</label>
                                <input {...destinationType} type="radio" value="Pod" />
                            </CdsRadio>
                            <CdsRadio key="service">
                                <label>Service</label>
                                <input {...destinationType} type="radio" value="Service" />
                            </CdsRadio>
                            <CdsRadio key="ipv4">
                                <label>IPv4</label>
                                <input {...destinationType} type="radio" value="IPv4" />
                            </CdsRadio>
                        </CdsRadioGroup>
                        <CdsInput>
                            <label>Destination Namespace</label>
                            <input {...register("dstNamespace")} defaultValue="default" />
                        </CdsInput>
                        <CdsInput>
                            <label>Destination</label>
                            <input {...register("dst", { required: "Destination is required" })} placeholder="Pod / Service Name, or IP" />
                        </CdsInput>
                        <ErrorMessage
                            errors={errors}
                            name="dst"
                            as={<ErrorMessageContainer />}
                        />
                        <CdsInput>
                            <label>Destination Port</label>
                            <input type="number" {...dstPort} />
                        </CdsInput>
                        <CdsSelect>
                            <label>Protocol</label>
                            <select {...register("protocol")}>
                                <option value="TCP">TCP</option>
                                <option value="UCP">UDP</option>
                                <option value="ICMP">ICMP</option>
                            </select>
                        </CdsSelect>
                        <CdsInput>
                            <label>Request Timeout</label>
                            <input type="number" {...timeout} placeholder="Timeout in seconds" />
                        </CdsInput>
                        <div cds-layout="horizontal gap:lg">
                            <CdsButton type="submit">Run Traceflow</CdsButton>
                            <CdsButton type="button" action="outline" onClick={()=> { reset(); navigate("/traceflow"); }}>Reset</CdsButton>
                        </div>
                        <TraceflowRunningAlert traceflowRunning={traceflowRunning} />
                    </CdsFormGroup>
                </form>
            </div>
            <div>
                <Outlet />
            </div>
            </div>
        </main>
    );
}
